package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tracing"
)

// nestedSpanObserver turns a nested-loop provider's callbacks (Claude CLI
// turns and tool calls) into child spans of the LLM call span that wraps the
// provider call. Tokens and cost on these spans are subtracted from the
// parent span in emitLLMSpanEnd, so trace totals are not counted twice.
type nestedSpanObserver struct {
	collector    *tracing.Collector
	traceID      uuid.UUID
	parentSpanID uuid.UUID
	agentID      *uuid.UUID
	teamID       *uuid.UUID
	tenantID     uuid.UUID
	provider     string
	previewLimit int

	mu    sync.Mutex
	spans map[string]nestedSpanRef // provider key -> span
}

type nestedSpanRef struct {
	id    uuid.UUID
	start time.Time
}

// newNestedSpanObserver returns nil when tracing is off for ctx or parentSpanID is nil.
func (l *Loop) newNestedSpanObserver(ctx context.Context, parentSpanID uuid.UUID, providerName string) *nestedSpanObserver {
	collector := tracing.CollectorFromContext(ctx)
	traceID := tracing.TraceIDFromContext(ctx)
	if collector == nil || traceID == uuid.Nil || parentSpanID == uuid.Nil {
		return nil
	}
	o := &nestedSpanObserver{
		collector:    collector,
		traceID:      traceID,
		parentSpanID: parentSpanID,
		teamID:       tracing.TraceTeamIDPtrFromContext(ctx),
		tenantID:     store.TenantIDFromContext(ctx),
		provider:     providerName,
		previewLimit: previewLimitForVerbose(collector.Verbose()),
		spans:        map[string]nestedSpanRef{},
	}
	if l.agentUUID != uuid.Nil {
		id := l.agentUUID
		o.agentID = &id
	}
	if o.tenantID == uuid.Nil {
		o.tenantID = store.MasterTenantID
	}
	return o
}

func (o *nestedSpanObserver) parentFor(parentKey string) *uuid.UUID {
	o.mu.Lock()
	defer o.mu.Unlock()
	if parentKey != "" {
		if ref, ok := o.spans[parentKey]; ok {
			id := ref.id
			return &id
		}
	}
	id := o.parentSpanID
	return &id
}

func (o *nestedSpanObserver) remember(key string, start time.Time) uuid.UUID {
	id := store.GenNewID()
	o.mu.Lock()
	o.spans[key] = nestedSpanRef{id: id, start: start}
	o.mu.Unlock()
	return id
}

func (o *nestedSpanObserver) lookup(key string) nestedSpanRef {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.spans[key]
}

func (o *nestedSpanObserver) endUpdates(ref nestedSpanRef, end time.Time) map[string]any {
	return map[string]any{
		"end_time":    end,
		"duration_ms": int(max(end.Sub(ref.start), 0).Milliseconds()),
		"status":      store.SpanStatusCompleted,
	}
}

func (o *nestedSpanObserver) baseSpan(id uuid.UUID, parent *uuid.UUID, spanType, name string, t0 time.Time) store.SpanData {
	return store.SpanData{
		ID:           id,
		TraceID:      o.traceID,
		ParentSpanID: parent,
		AgentID:      o.agentID,
		TeamID:       o.teamID,
		TenantID:     o.tenantID,
		SpanType:     spanType,
		Name:         name,
		StartTime:    t0,
		Status:       store.SpanStatusRunning,
		Level:        store.SpanLevelDefault,
		CreatedAt:    t0,
	}
}

func (o *nestedSpanObserver) LLMTurnStart(t providers.NestedLLMTurnStart) {
	parent := o.parentFor(t.ParentKey)
	id := o.remember(t.Key, t.Start)
	name := fmt.Sprintf("%s/%s #%d", o.provider, t.Model, t.Iteration)
	if t.ParentKey != "" {
		name = fmt.Sprintf("%s/%s subagent #%d", o.provider, t.Model, t.Iteration)
	}
	span := o.baseSpan(id, parent, store.SpanTypeLLMCall, name, t.Start)
	span.Model = t.Model
	span.Provider = o.provider
	span.Metadata = mustJSON(map[string]any{"nested_source": "claude_cli", "cli_message_id": t.Key})
	o.collector.EmitSpan(span)
}

func (o *nestedSpanObserver) LLMTurnEnd(t providers.NestedLLMTurnEnd) {
	ref := o.lookup(t.Key)
	if ref.id == uuid.Nil {
		return
	}
	updates := o.endUpdates(ref, t.End)
	updates["finish_reason"] = t.FinishReason
	updates["input_tokens"] = t.Usage.PromptTokens
	updates["output_tokens"] = t.Usage.CompletionTokens
	preview := t.Content
	if t.Thinking != "" {
		preview = "<thinking>\n" + t.Thinking + "\n</thinking>\n" + t.Content
	}
	if preview != "" {
		updates["output_preview"] = tracing.TruncateMid(preview, o.previewLimit)
	}
	meta := map[string]any{"nested_source": "claude_cli", "cli_message_id": t.Key}
	if t.Usage.CacheCreationTokens > 0 {
		meta["cache_creation_tokens"] = t.Usage.CacheCreationTokens
	}
	if t.Usage.CacheReadTokens > 0 {
		meta["cache_read_tokens"] = t.Usage.CacheReadTokens
	}
	if t.ToolCalls > 0 {
		meta["tool_calls"] = t.ToolCalls
	}
	if t.OutputTokensPartial {
		meta["output_tokens_partial"] = true
	}
	updates["metadata"] = mustJSON(meta)
	if t.Error != "" {
		updates["status"] = store.SpanStatusError
		updates["error"] = t.Error
	}
	o.collector.EmitSpanUpdate(ref.id, o.traceID, updates)
}

func (o *nestedSpanObserver) ToolCallStart(t providers.NestedToolCallStart) {
	parent := o.parentFor(t.ParentKey)
	id := o.remember(t.Key, t.Start)
	span := o.baseSpan(id, parent, store.SpanTypeToolCall, t.Name, t.Start)
	span.ToolName = t.Name
	span.ToolCallID = t.Key
	span.InputPreview = tracing.TruncateJSON(t.Input, o.previewLimit)
	span.Metadata = mustJSON(map[string]any{"nested_source": "claude_cli"})
	o.collector.EmitSpan(span)
}

func (o *nestedSpanObserver) ToolCallEnd(t providers.NestedToolCallEnd) {
	ref := o.lookup(t.Key)
	if ref.id == uuid.Nil {
		return
	}
	updates := o.endUpdates(ref, t.End)
	updates["output_preview"] = tracing.TruncateMid(t.Output, o.previewLimit)
	if t.IsError {
		updates["status"] = store.SpanStatusError
		updates["error"] = truncateStr(t.Output, 200)
	}
	o.collector.EmitSpanUpdate(ref.id, o.traceID, updates)
}

func (o *nestedSpanObserver) TurnCosts(costs map[string]float64) {
	for key, cost := range costs {
		ref := o.lookup(key)
		if ref.id == uuid.Nil || cost <= 0 {
			continue
		}
		o.collector.EmitSpanUpdate(ref.id, o.traceID, map[string]any{"total_cost": cost})
	}
}
