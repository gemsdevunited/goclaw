package providers

import (
	"context"
	"time"
)

// NestedTraceObserver receives the inner activity of a provider that runs its
// own agent loop out of process (Claude CLI). The agent loop attaches one per
// LLM call via WithNestedTraceObserver and turns the callbacks into child
// llm_call / tool_call spans under that call's span.
//
// Keys are provider-scoped strings (CLI message id, tool_use id). parentKey
// is the key of the tool call that spawned the activity (a CLI Task subagent),
// or "" for top-level activity.
//
// Callbacks run on the provider's stream-reading goroutine; implementations
// must not block.
type NestedTraceObserver interface {
	LLMTurnStart(t NestedLLMTurnStart)
	LLMTurnEnd(t NestedLLMTurnEnd)
	ToolCallStart(t NestedToolCallStart)
	ToolCallEnd(t NestedToolCallEnd)
	// TurnCosts assigns cost (USD) to turns that already ended. Sent once,
	// after the provider learns the run's total cost.
	TurnCosts(costs map[string]float64)
}

// NestedLLMTurnStart opens one model request inside the provider's loop.
type NestedLLMTurnStart struct {
	Key       string
	ParentKey string
	Model     string
	Iteration int // 1-based, per lane
	Start     time.Time
}

// NestedLLMTurnEnd closes a turn opened with LLMTurnStart.
type NestedLLMTurnEnd struct {
	Key          string
	End          time.Time
	Usage        Usage // input excludes cache; cache counts in Cache*Tokens
	Content      string
	Thinking     string
	ToolCalls    int
	FinishReason string
	Error        string
	// OutputTokensPartial means Usage.CompletionTokens is a mid-stream
	// snapshot (Claude CLI); the run total is on the parent span.
	OutputTokensPartial bool
}

// NestedToolCallStart opens one tool execution inside the provider's loop.
type NestedToolCallStart struct {
	Key       string // tool_use id
	ParentKey string
	Name      string
	Input     string // JSON
	Start     time.Time
}

// NestedToolCallEnd closes a tool call opened with ToolCallStart.
type NestedToolCallEnd struct {
	Key     string
	End     time.Time
	Output  string
	IsError bool
}

// ProviderTrace is what a nested-loop provider reports about the whole call,
// for the trace only. It does not feed ChatResponse.Usage, so token accounting
// for context, compaction and usage caps is unchanged.
type ProviderTrace struct {
	// TotalUsage is the provider's own total for the call, cache included.
	TotalUsage Usage
	// CostUSD is the provider-reported cost for the call (0 = unknown).
	CostUSD float64
	// Nested is the part of TotalUsage / CostUSD already recorded on child
	// spans through a NestedTraceObserver. The parent span records the rest.
	Nested NestedTraceTotals
	// NumTurns, DurationAPIMS are provider-reported run stats (0 = unknown).
	NumTurns      int
	DurationAPIMS int
}

// NestedTraceTotals sums what child spans recorded.
type NestedTraceTotals struct {
	Usage     Usage
	CostUSD   float64
	LLMTurns  int
	ToolCalls int
}

// RemainderUsage returns TotalUsage minus what child spans already recorded,
// clamped at zero per counter.
func (pt *ProviderTrace) RemainderUsage() Usage {
	sub := func(a, b int) int { return max(a-b, 0) }
	return Usage{
		PromptTokens:        sub(pt.TotalUsage.PromptTokens, pt.Nested.Usage.PromptTokens),
		CompletionTokens:    sub(pt.TotalUsage.CompletionTokens, pt.Nested.Usage.CompletionTokens),
		CacheCreationTokens: sub(pt.TotalUsage.CacheCreationTokens, pt.Nested.Usage.CacheCreationTokens),
		CacheReadTokens:     sub(pt.TotalUsage.CacheReadTokens, pt.Nested.Usage.CacheReadTokens),
	}
}

// RemainderCostUSD returns CostUSD minus what child spans recorded, clamped at zero.
func (pt *ProviderTrace) RemainderCostUSD() float64 {
	return max(pt.CostUSD-pt.Nested.CostUSD, 0)
}

type nestedTraceObserverKey struct{}

// WithNestedTraceObserver attaches obs for the next provider call made with ctx.
func WithNestedTraceObserver(ctx context.Context, obs NestedTraceObserver) context.Context {
	if obs == nil {
		return ctx
	}
	return context.WithValue(ctx, nestedTraceObserverKey{}, obs)
}

// NestedTraceObserverFromContext returns the attached observer, or nil.
func NestedTraceObserverFromContext(ctx context.Context) NestedTraceObserver {
	obs, _ := ctx.Value(nestedTraceObserverKey{}).(NestedTraceObserver)
	return obs
}
