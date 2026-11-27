package providers

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// cliTraceTracker turns Claude CLI stream-json events into nested trace
// callbacks: one LLM turn per API message (events sharing message.id), one
// tool call per tool_use id (closed by the matching tool_result).
//
// The CLI emits one "assistant" event per content block, so a single API
// response arrives as several events with the same message.id and the same
// usage. Turns are keyed by message.id to avoid counting usage twice.
//
// Events from a CLI Task subagent carry parent_tool_use_id; each value is its
// own lane so subagent turns nest under the Task tool call.
//
// Not safe for concurrent use; the stream reader drives it from one goroutine.
type cliTraceTracker struct {
	obs NestedTraceObserver // nil = only compute totals
	now func() time.Time

	runStart  time.Time
	lanes     map[string]*cliTraceLane
	tools     map[string]*cliTraceTool // open + closed, by tool_use id
	turns     []*cliTraceTurn          // closed turns, in close order
	anonTurns int
}

type cliTraceLane struct {
	boundary  time.Time // when the lane's next request was sent (start, or last tool results)
	cur       *cliTraceTurn
	iteration int
}

type cliTraceTurn struct {
	key, parent, model string
	lastSeen           time.Time
	usage              Usage
	text, thinking     strings.Builder
	toolCalls          int
	stopReason         string
}

type cliTraceTool struct {
	start  time.Time
	closed bool
}

func newCLITraceTracker(obs NestedTraceObserver, now func() time.Time) *cliTraceTracker {
	if now == nil {
		now = time.Now
	}
	start := now().UTC()
	return &cliTraceTracker{
		obs:      obs,
		now:      func() time.Time { return now().UTC() },
		runStart: start,
		lanes:    map[string]*cliTraceLane{},
		tools:    map[string]*cliTraceTool{},
	}
}

func (t *cliTraceTracker) lane(parent string) *cliTraceLane {
	if l, ok := t.lanes[parent]; ok {
		return l
	}
	boundary := t.runStart
	if parent != "" {
		if tool, ok := t.tools[parent]; ok {
			boundary = tool.start
		}
	}
	l := &cliTraceLane{boundary: boundary}
	t.lanes[parent] = l
	return l
}

// onAssistant handles one "assistant" stream event.
func (t *cliTraceTracker) onAssistant(msg *cliStreamMsg, parent string) {
	if msg == nil {
		return
	}
	now := t.now()
	l := t.lane(parent)
	if l.cur != nil && msg.ID != "" && l.cur.key != msg.ID {
		t.closeTurn(l, "")
	}
	if l.cur == nil {
		key := msg.ID
		if key == "" {
			t.anonTurns++
			key = fmt.Sprintf("cli-turn-%d", t.anonTurns)
		}
		l.iteration++
		l.cur = &cliTraceTurn{key: key, parent: parent, model: msg.Model}
		if t.obs != nil {
			t.obs.LLMTurnStart(NestedLLMTurnStart{
				Key: key, ParentKey: parent, Model: msg.Model, Iteration: l.iteration, Start: l.boundary,
			})
		}
	}
	turn := l.cur
	turn.lastSeen = now
	if msg.Model != "" {
		turn.model = msg.Model
	}
	if msg.Usage != nil {
		turn.usage = msg.Usage.toTraceUsage() // every event of a message repeats it; keep the latest
	}
	if msg.StopReason != "" {
		turn.stopReason = msg.StopReason
	}
	for i := range msg.Content {
		b := &msg.Content[i]
		switch b.Type {
		case "text":
			turn.text.WriteString(b.Text)
		case "thinking":
			turn.thinking.WriteString(b.Thinking)
		case "tool_use":
			if b.ID == "" {
				continue
			}
			if _, seen := t.tools[b.ID]; seen {
				continue
			}
			t.tools[b.ID] = &cliTraceTool{start: now}
			turn.toolCalls++
			if t.obs != nil {
				t.obs.ToolCallStart(NestedToolCallStart{
					Key: b.ID, ParentKey: parent, Name: b.Name, Input: string(b.Input), Start: now,
				})
			}
		}
	}
}

// onUser handles one "user" stream event (tool results fed back to the model).
func (t *cliTraceTracker) onUser(msg *cliStreamMsg, parent string) {
	if msg == nil {
		return
	}
	now := t.now()
	l := t.lane(parent)
	hasResult := false
	for i := range msg.Content {
		b := &msg.Content[i]
		if b.Type != "tool_result" || b.ToolUseID == "" {
			continue
		}
		hasResult = true
		tool, ok := t.tools[b.ToolUseID]
		if !ok || tool.closed {
			continue
		}
		tool.closed = true
		if sub, ok := t.lanes[b.ToolUseID]; ok {
			t.closeTurn(sub, "") // a Task subagent's last turn ends with its tool result
		}
		if t.obs != nil {
			t.obs.ToolCallEnd(NestedToolCallEnd{
				Key: b.ToolUseID, End: now, Output: b.toolResultText(), IsError: b.IsError,
			})
		}
	}
	if hasResult {
		t.closeTurn(l, "")
		l.boundary = now
	}
}

func (t *cliTraceTracker) closeTurn(l *cliTraceLane, errMsg string) {
	turn := l.cur
	if turn == nil {
		return
	}
	l.cur = nil
	t.turns = append(t.turns, turn)
	if t.obs == nil {
		return
	}
	finish := turn.stopReason
	if finish == "" {
		if turn.toolCalls > 0 {
			finish = "tool_use"
		} else {
			finish = "end_turn"
		}
	}
	if errMsg != "" {
		finish = "error"
	}
	t.obs.LLMTurnEnd(NestedLLMTurnEnd{
		Key:          turn.key,
		End:          turn.lastSeen,
		Usage:        turn.usage,
		Content:      turn.text.String(),
		Thinking:     turn.thinking.String(),
		ToolCalls:    turn.toolCalls,
		FinishReason: finish,
		Error:        errMsg,
	})
}

// closeAll ends open turns and tool calls. errMsg marks them failed.
func (t *cliTraceTracker) closeAll(errMsg string) {
	parents := make([]string, 0, len(t.lanes))
	for p := range t.lanes {
		parents = append(parents, p)
	}
	sort.Strings(parents) // deterministic callback order
	for _, p := range parents {
		t.closeTurn(t.lanes[p], errMsg)
	}
	ids := make([]string, 0, len(t.tools))
	for id, tool := range t.tools {
		if !tool.closed {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	now := t.now()
	for _, id := range ids {
		t.tools[id].closed = true
		if t.obs == nil {
			continue
		}
		out := errMsg
		if out == "" {
			out = "no tool_result received before the CLI run ended"
		}
		t.obs.ToolCallEnd(NestedToolCallEnd{Key: id, End: now, Output: out, IsError: true})
	}
}

// abort closes everything as failed (context cancel, process error).
func (t *cliTraceTracker) abort(errMsg string) {
	if errMsg == "" {
		errMsg = "claude-cli run aborted"
	}
	t.closeAll(errMsg)
}

// finish closes open spans, assigns per-turn cost from the result event and
// returns the call's trace totals. ev may be nil when no result event came.
func (t *cliTraceTracker) finish(ev *cliStreamEvent) *ProviderTrace {
	t.closeAll("")

	pt := &ProviderTrace{}
	for _, turn := range t.turns {
		addUsage(&pt.Nested.Usage, turn.usage)
	}
	pt.Nested.LLMTurns = len(t.turns)
	pt.Nested.ToolCalls = len(t.tools)

	if ev == nil {
		pt.TotalUsage = pt.Nested.Usage
		if t.obs == nil {
			pt.Nested = NestedTraceTotals{}
		}
		return pt
	}
	if ev.Usage != nil {
		pt.TotalUsage = ev.Usage.toTraceUsage()
	} else {
		pt.TotalUsage = pt.Nested.Usage
	}
	pt.CostUSD = cliCostUSD(ev.TotalCostUSD, ev.CostUSD)
	pt.NumTurns = ev.NumTurns
	pt.DurationAPIMS = ev.DurationAPIMS

	if t.obs == nil {
		// No child spans were emitted: the parent span records everything.
		pt.Nested = NestedTraceTotals{}
		return pt
	}
	if costs := allocateCLITurnCosts(t.turns, pt.CostUSD, ev.ModelUsage); len(costs) > 0 {
		for _, c := range costs {
			pt.Nested.CostUSD += c
		}
		t.obs.TurnCosts(costs)
	}
	return pt
}

func addUsage(dst *Usage, u Usage) {
	dst.PromptTokens += u.PromptTokens
	dst.CompletionTokens += u.CompletionTokens
	dst.TotalTokens += u.TotalTokens
	dst.CacheCreationTokens += u.CacheCreationTokens
	dst.CacheReadTokens += u.CacheReadTokens
}

// cliUsageWeight approximates a turn's relative price. Claude pricing is
// linear in these counters with the same ratios for every model
// (output 5x input, cache write 1.25x, cache read 0.1x), so shares by weight
// within one model reproduce the real split.
func cliUsageWeight(u Usage) float64 {
	return float64(u.PromptTokens) +
		1.25*float64(u.CacheCreationTokens) +
		0.1*float64(u.CacheReadTokens) +
		5*float64(u.CompletionTokens)
}

// allocateCLITurnCosts splits the CLI-reported cost over turns.
//
// With modelUsage, each model's costUSD is split over that model's turns;
// a model with no visible turns (CLI background calls) keeps its cost
// unallocated so it stays on the parent span. Without modelUsage the total
// is split over all turns.
func allocateCLITurnCosts(turns []*cliTraceTurn, total float64, modelUsage map[string]cliModelUsage) map[string]float64 {
	if total <= 0 || len(turns) == 0 {
		return nil
	}
	costs := map[string]float64{}
	split := func(group []*cliTraceTurn, amount float64) {
		if amount <= 0 || len(group) == 0 {
			return
		}
		var sum float64
		for _, turn := range group {
			sum += cliUsageWeight(turn.usage)
		}
		for _, turn := range group {
			share := 1 / float64(len(group))
			if sum > 0 {
				share = cliUsageWeight(turn.usage) / sum
			}
			costs[turn.key] += amount * share
		}
	}

	if len(modelUsage) == 0 {
		split(turns, total)
		return costs
	}

	byModel := map[string][]*cliTraceTurn{}
	var unmatched []*cliTraceTurn
	for _, turn := range turns {
		if m := matchCLIModelUsage(turn.model, modelUsage); m != "" {
			byModel[m] = append(byModel[m], turn)
		} else {
			unmatched = append(unmatched, turn)
		}
	}
	allocated := 0.0
	for m, group := range byModel {
		split(group, modelUsage[m].CostUSD)
		allocated += modelUsage[m].CostUSD
	}
	if len(unmatched) > 0 {
		// Cost of models that had no visible turns is not ours to spread;
		// only give unmatched turns what no model entry accounts for.
		accounted := allocated
		for m, mu := range modelUsage {
			if _, ok := byModel[m]; !ok {
				accounted += mu.CostUSD
			}
		}
		split(unmatched, total-accounted)
	}
	return costs
}

// matchCLIModelUsage finds the modelUsage key for a message model id.
func matchCLIModelUsage(model string, modelUsage map[string]cliModelUsage) string {
	if model == "" {
		return ""
	}
	if _, ok := modelUsage[model]; ok {
		return model
	}
	keys := make([]string, 0, len(modelUsage))
	for k := range modelUsage {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if strings.HasPrefix(k, model) || strings.HasPrefix(model, k) {
			return k
		}
	}
	return ""
}
