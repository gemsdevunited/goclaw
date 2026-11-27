package agent

import (
	"context"
	"encoding/json"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tracing"
)

// spanRecordingStore keeps spans in memory and applies updates the way the
// SQL stores do (column overwrite).
type spanRecordingStore struct {
	store.TracingStore // unimplemented methods panic
	mu                 sync.Mutex
	spans              map[uuid.UUID]map[string]any
	order              []uuid.UUID
}

func newSpanRecordingStore() *spanRecordingStore {
	return &spanRecordingStore{spans: map[uuid.UUID]map[string]any{}}
}

func (s *spanRecordingStore) BatchCreateSpans(_ context.Context, spans []store.SpanData) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sp := range spans {
		row := map[string]any{
			"span_type": sp.SpanType, "name": sp.Name, "status": sp.Status,
			"tool_name": sp.ToolName, "tool_call_id": sp.ToolCallID, "model": sp.Model,
		}
		if sp.ParentSpanID != nil {
			row["parent_span_id"] = *sp.ParentSpanID
		}
		s.spans[sp.ID] = row
		s.order = append(s.order, sp.ID)
	}
	return nil
}

func (s *spanRecordingStore) UpdateSpan(_ context.Context, id uuid.UUID, updates map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.spans[id]
	if !ok {
		return nil
	}
	for k, v := range updates {
		row[k] = v
	}
	return nil
}

func (s *spanRecordingStore) BatchUpdateTraceAggregates(context.Context, uuid.UUID) error { return nil }
func (s *spanRecordingStore) DeleteTracesOlderThan(context.Context, time.Time) (int64, error) {
	return 0, nil
}

func asInt(v any) int {
	n, _ := v.(int)
	return n
}

func asFloat(v any) float64 {
	f, _ := v.(float64)
	return f
}

func metaInt(row map[string]any, key string) int {
	raw, ok := row["metadata"].(json.RawMessage)
	if !ok {
		return 0
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	f, _ := m[key].(float64)
	return int(f)
}

func TestNestedSpanObserverChildSpansAndNoDoubleCount(t *testing.T) {
	st := newSpanRecordingStore()
	collector := tracing.NewCollector(st)
	collector.Start()

	traceID := uuid.New()
	ctx := tracing.WithCollector(context.Background(), collector)
	ctx = tracing.WithTraceID(ctx, traceID)

	l := &Loop{agentUUID: uuid.New(), model: "opus", provider: &stubProvider{}}
	parentID := uuid.New()
	collector.EmitSpan(store.SpanData{ID: parentID, TraceID: traceID, SpanType: store.SpanTypeLLMCall, Status: store.SpanStatusRunning})

	obs := l.newNestedSpanObserver(ctx, parentID, "claudecode")
	if obs == nil {
		t.Fatal("observer nil with tracing on")
	}
	t0 := time.Now().UTC()
	obs.LLMTurnStart(providers.NestedLLMTurnStart{Key: "m1", Model: "claude-opus", Iteration: 1, Start: t0})
	obs.ToolCallStart(providers.NestedToolCallStart{Key: "tu1", Name: "Task", Input: `{"prompt":"x"}`, Start: t0.Add(time.Second)})
	obs.LLMTurnEnd(providers.NestedLLMTurnEnd{Key: "m1", End: t0.Add(time.Second), FinishReason: "tool_use", ToolCalls: 1,
		Usage: providers.Usage{PromptTokens: 10, CompletionTokens: 100, CacheReadTokens: 1000, CacheCreationTokens: 50}})
	obs.LLMTurnStart(providers.NestedLLMTurnStart{Key: "s1", ParentKey: "tu1", Model: "claude-opus", Iteration: 1, Start: t0.Add(time.Second)})
	obs.LLMTurnEnd(providers.NestedLLMTurnEnd{Key: "s1", End: t0.Add(2 * time.Second), Usage: providers.Usage{PromptTokens: 5, CompletionTokens: 20}})
	obs.ToolCallEnd(providers.NestedToolCallEnd{Key: "tu1", End: t0.Add(3 * time.Second), Output: "done"})
	obs.TurnCosts(map[string]float64{"m1": 0.3, "s1": 0.1})

	// Parent span end with the CLI's own totals.
	resp := &providers.ChatResponse{
		Content: "ok", FinishReason: "stop",
		Usage: &providers.Usage{PromptTokens: 15, CompletionTokens: 150},
		ProviderTrace: &providers.ProviderTrace{
			TotalUsage: providers.Usage{PromptTokens: 15, CompletionTokens: 150, CacheReadTokens: 1000, CacheCreationTokens: 50},
			CostUSD:    0.45,
			Nested: providers.NestedTraceTotals{
				Usage:   providers.Usage{PromptTokens: 15, CompletionTokens: 120, CacheReadTokens: 1000, CacheCreationTokens: 50},
				CostUSD: 0.4, LLMTurns: 2, ToolCalls: 1,
			},
			NumTurns: 2,
		},
	}
	l.emitLLMSpanEnd(ctx, parentID, t0, resp, nil, withProvider("claudecode"))
	collector.Stop()

	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.order) != 4 {
		t.Fatalf("spans = %d, want parent + 2 turns + 1 tool", len(st.order))
	}
	byName := map[string]map[string]any{}
	for _, id := range st.order {
		row := st.spans[id]
		key := row["name"].(string)
		if row["span_type"] == store.SpanTypeToolCall {
			key = "tool:" + row["tool_call_id"].(string)
		}
		row["id"] = id
		byName[key] = row
	}
	turn1 := byName["claudecode/claude-opus #1"]
	tool := byName["tool:tu1"]
	if turn1 == nil || tool == nil {
		t.Fatalf("missing spans: %v", byName)
	}
	if turn1["parent_span_id"] != parentID || tool["parent_span_id"] != parentID {
		t.Errorf("top-level children must hang off the LLM call span")
	}
	// The subagent turn nests under the Task tool span.
	var sub map[string]any
	for _, row := range st.spans {
		if row["parent_span_id"] == tool["id"] {
			sub = row
		}
	}
	if sub == nil || sub["span_type"] != store.SpanTypeLLMCall {
		t.Fatalf("subagent turn not nested under Task tool span")
	}
	if tool["status"] != store.SpanStatusCompleted || tool["output_preview"] != "done" || asInt(tool["duration_ms"]) != 2000 {
		t.Errorf("tool span = %v", tool)
	}

	// Sum of input/output/cache/cost over llm_call + tool_call spans, as
	// BatchUpdateTraceAggregates does, must equal the CLI totals exactly.
	var in, out, cacheRead, cacheWrite int
	var cost float64
	for _, row := range st.spans {
		in += asInt(row["input_tokens"])
		out += asInt(row["output_tokens"])
		cacheRead += metaInt(row, "cache_read_tokens")
		cacheWrite += metaInt(row, "cache_creation_tokens")
		cost += asFloat(row["total_cost"])
	}
	if in != 15 || out != 150 || cacheRead != 1000 || cacheWrite != 50 {
		t.Errorf("aggregate tokens in=%d out=%d cacheRead=%d cacheWrite=%d, want 15/150/1000/50", in, out, cacheRead, cacheWrite)
	}
	if math.Abs(cost-0.45) > 1e-9 {
		t.Errorf("aggregate cost = %v, want 0.45", cost)
	}
	parent := st.spans[parentID]
	if asInt(parent["output_tokens"]) != 30 || asInt(parent["input_tokens"]) != 0 {
		t.Errorf("parent span should hold only the remainder, got in=%v out=%v", parent["input_tokens"], parent["output_tokens"])
	}
	if metaInt(parent, "cache_read_tokens") != 0 {
		t.Errorf("parent must not repeat cache tokens already on children")
	}
}

func TestLLMSpanUsageRegularProviderUnchanged(t *testing.T) {
	u := &providers.Usage{PromptTokens: 7, CompletionTokens: 3}
	got, cost, meta := llmSpanUsage(&providers.ChatResponse{Usage: u})
	if got != u || cost != nil || meta != nil {
		t.Fatalf("regular provider: usage=%v cost=%v meta=%v", got, cost, meta)
	}
}

func TestNewNestedSpanObserverNilWithoutTracing(t *testing.T) {
	l := &Loop{}
	if obs := l.newNestedSpanObserver(context.Background(), uuid.New(), "claudecode"); obs != nil {
		t.Fatal("observer must be nil without a collector")
	}
}
