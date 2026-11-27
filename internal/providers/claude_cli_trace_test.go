package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// testdata/claude_cli_stream_tools.jsonl follows the Claude Code
// `-p --output-format stream-json --verbose` shape: one "assistant" event per
// content block (blocks of one API response share message.id and usage),
// "user" events carrying tool_result blocks, a Task subagent lane marked by
// parent_tool_use_id, and a final "result" with total_cost_usd / modelUsage.

type recordedObserver struct {
	mu         sync.Mutex
	turnStarts []NestedLLMTurnStart
	turnEnds   []NestedLLMTurnEnd
	toolStarts []NestedToolCallStart
	toolEnds   []NestedToolCallEnd
	costs      map[string]float64
}

func (r *recordedObserver) LLMTurnStart(t NestedLLMTurnStart) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.turnStarts = append(r.turnStarts, t)
}
func (r *recordedObserver) LLMTurnEnd(t NestedLLMTurnEnd) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.turnEnds = append(r.turnEnds, t)
}
func (r *recordedObserver) ToolCallStart(t NestedToolCallStart) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.toolStarts = append(r.toolStarts, t)
}
func (r *recordedObserver) ToolCallEnd(t NestedToolCallEnd) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.toolEnds = append(r.toolEnds, t)
}
func (r *recordedObserver) TurnCosts(c map[string]float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.costs = c
}

// fakeClaudeCLI writes a script that ignores its args and prints fixture.
func fakeClaudeCLI(t *testing.T, fixture string, exitCode int) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake CLI")
	}
	abs, err := filepath.Abs(fixture)
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "claude")
	body := fmt.Sprintf("#!/bin/sh\ncat '%s'\nexit %d\n", abs, exitCode)
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

func runFakeStream(t *testing.T, ctx context.Context, fixture string) (*ChatResponse, string) {
	t.Helper()
	p := NewClaudeCLIProvider(fakeClaudeCLI(t, fixture, 0), WithClaudeCLIWorkDir(t.TempDir()))
	var streamed strings.Builder
	resp, err := p.ChatStream(ctx, ChatRequest{
		Messages: []Message{{Role: "user", Content: "review"}},
		Options:  map[string]any{OptSessionKey: "trace-test"},
	}, func(c StreamChunk) { streamed.WriteString(c.Content) })
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	return resp, streamed.String()
}

func TestClaudeCLIChatStreamResponseUnchangedByTracing(t *testing.T) {
	fixture := "testdata/claude_cli_stream_tools.jsonl"
	plain, plainStream := runFakeStream(t, context.Background(), fixture)
	traced, tracedStream := runFakeStream(t, WithNestedTraceObserver(context.Background(), &recordedObserver{}), fixture)

	if plain.Content != "Review posted: 1 issue." || traced.Content != plain.Content {
		t.Fatalf("content: plain=%q traced=%q", plain.Content, traced.Content)
	}
	if plainStream != tracedStream {
		t.Fatalf("streamed chunks differ: %q vs %q", plainStream, tracedStream)
	}
	// ChatResponse.Usage keeps the input/output-only mapping (agent behavior unchanged).
	for name, r := range map[string]*ChatResponse{"plain": plain, "traced": traced} {
		u := r.Usage
		if u == nil || u.PromptTokens != 24 || u.CompletionTokens != 400 || u.CacheReadTokens != 0 || u.CacheCreationTokens != 0 {
			t.Fatalf("%s Usage = %+v, want input 24 / output 400 / no cache", name, u)
		}
	}
	// Without an observer nothing is recorded on child spans.
	if plain.ProviderTrace == nil || plain.ProviderTrace.Nested != (NestedTraceTotals{}) {
		t.Fatalf("plain ProviderTrace.Nested = %+v, want zero", plain.ProviderTrace)
	}
	if got := plain.ProviderTrace.RemainderUsage(); got.CacheReadTokens != 50300 || got.CompletionTokens != 400 {
		t.Fatalf("plain remainder = %+v, want full totals", got)
	}
	if plain.ProviderTrace.RemainderCostUSD() != 0.5 {
		t.Fatalf("plain remainder cost = %v, want 0.5", plain.ProviderTrace.RemainderCostUSD())
	}
}

func TestClaudeCLIChatStreamNestedTrace(t *testing.T) {
	obs := &recordedObserver{}
	resp, _ := runFakeStream(t, WithNestedTraceObserver(context.Background(), obs), "testdata/claude_cli_stream_tools.jsonl")

	// One turn per message id, despite msg_A arriving as three events.
	wantTurns := []string{"msg_A", "msg_B", "msg_S1", "msg_S2", "msg_C"}
	if len(obs.turnStarts) != len(wantTurns) {
		t.Fatalf("turn starts = %d (%+v), want %d", len(obs.turnStarts), obs.turnStarts, len(wantTurns))
	}
	parents := map[string]string{"msg_A": "", "msg_B": "", "msg_S1": "toolu_3", "msg_S2": "toolu_3", "msg_C": ""}
	for i, s := range obs.turnStarts {
		if s.Key != wantTurns[i] || s.ParentKey != parents[s.Key] {
			t.Errorf("turn %d = %s parent %q, want %s parent %q", i, s.Key, s.ParentKey, wantTurns[i], parents[wantTurns[i]])
		}
		if s.Model != "claude-opus-4-5-20251101" {
			t.Errorf("turn %s model = %q", s.Key, s.Model)
		}
	}
	if len(obs.turnEnds) != len(wantTurns) {
		t.Fatalf("turn ends = %d, want %d", len(obs.turnEnds), len(wantTurns))
	}
	ends := map[string]NestedLLMTurnEnd{}
	for _, e := range obs.turnEnds {
		ends[e.Key] = e
	}
	a := ends["msg_A"]
	if a.Usage.PromptTokens != 10 || a.Usage.CompletionTokens != 120 || a.Usage.CacheReadTokens != 15000 || a.Usage.CacheCreationTokens != 2000 {
		t.Errorf("msg_A usage = %+v (must be counted once)", a.Usage)
	}
	if a.ToolCalls != 2 || a.Thinking != "Need to read the diff." || a.FinishReason != "tool_use" {
		t.Errorf("msg_A = %+v", a)
	}
	if c := ends["msg_C"]; c.Content != "Review posted: 1 issue." || c.FinishReason != "end_turn" {
		t.Errorf("msg_C = %+v", c)
	}

	// Tools open and close by id; the subagent's Bash nests under Task.
	if len(obs.toolStarts) != 4 || len(obs.toolEnds) != 4 {
		t.Fatalf("tool starts/ends = %d/%d, want 4/4", len(obs.toolStarts), len(obs.toolEnds))
	}
	starts := map[string]NestedToolCallStart{}
	for _, s := range obs.toolStarts {
		starts[s.Key] = s
	}
	if s := starts["toolu_1"]; s.Name != "Bash" || s.Input != `{"command":"git diff --stat"}` || s.ParentKey != "" {
		t.Errorf("toolu_1 start = %+v", s)
	}
	if s := starts["toolu_4"]; s.ParentKey != "toolu_3" {
		t.Errorf("toolu_4 parent = %q, want toolu_3", s.ParentKey)
	}
	toolEnds := map[string]NestedToolCallEnd{}
	for _, e := range obs.toolEnds {
		toolEnds[e.Key] = e
	}
	if e := toolEnds["toolu_2"]; e.Output != "package a" || e.IsError {
		t.Errorf("toolu_2 end = %+v", e)
	}
	if e := toolEnds["toolu_4"]; !e.IsError || !strings.Contains(e.Output, "FAIL a") {
		t.Errorf("toolu_4 end = %+v", e)
	}

	// Totals: CLI-reported, cache included; children hold the per-turn part.
	pt := resp.ProviderTrace
	if pt == nil {
		t.Fatal("ProviderTrace nil")
	}
	want := Usage{PromptTokens: 24, CompletionTokens: 400, TotalTokens: 424, CacheCreationTokens: 3400, CacheReadTokens: 50300}
	if pt.TotalUsage != want {
		t.Errorf("TotalUsage = %+v, want %+v", pt.TotalUsage, want)
	}
	if pt.CostUSD != 0.5 || pt.NumTurns != 5 || pt.DurationAPIMS != 30000 {
		t.Errorf("run stats = cost %v turns %d api %d", pt.CostUSD, pt.NumTurns, pt.DurationAPIMS)
	}
	if pt.Nested.LLMTurns != 5 || pt.Nested.ToolCalls != 4 {
		t.Errorf("nested counts = %+v", pt.Nested)
	}
	rem := pt.RemainderUsage()
	if rem.PromptTokens != 0 || rem.CacheReadTokens != 0 || rem.CacheCreationTokens != 0 || rem.CompletionTokens != 130 {
		t.Errorf("remainder = %+v, want only 130 output tokens", rem)
	}

	// Opus cost (0.45) goes to the opus turns; haiku (0.05, no visible turns)
	// stays on the parent span.
	var sum float64
	for _, c := range obs.costs {
		sum += c
	}
	if len(obs.costs) != 5 || math.Abs(sum-0.45) > 1e-9 {
		t.Errorf("turn costs = %v (sum %v), want 5 turns summing to 0.45", obs.costs, sum)
	}
	if math.Abs(pt.RemainderCostUSD()-0.05) > 1e-9 {
		t.Errorf("remainder cost = %v, want 0.05", pt.RemainderCostUSD())
	}
	if obs.costs["msg_A"] <= obs.costs["msg_S2"] {
		t.Errorf("cost split not weighted by usage: %v", obs.costs)
	}
}

func TestClaudeCLITraceAbortClosesOpenSpans(t *testing.T) {
	obs := &recordedObserver{}
	tr := newCLITraceTracker(obs, nil)
	tr.onAssistant(&cliStreamMsg{ID: "m1", Model: "opus", Content: []cliContentBlock{
		{Type: "tool_use", ID: "t1", Name: "Bash"},
	}}, "", "")
	tr.abort("context canceled")

	if len(obs.turnEnds) != 1 || obs.turnEnds[0].Error != "context canceled" || obs.turnEnds[0].FinishReason != "error" {
		t.Fatalf("turn ends = %+v", obs.turnEnds)
	}
	if len(obs.toolEnds) != 1 || !obs.toolEnds[0].IsError || obs.toolEnds[0].Output != "context canceled" {
		t.Fatalf("tool ends = %+v", obs.toolEnds)
	}
}

func TestAllocateCLITurnCostsWithoutModelUsage(t *testing.T) {
	turns := []*cliTraceTurn{
		{key: "a", usage: Usage{PromptTokens: 100}},
		{key: "b", usage: Usage{PromptTokens: 300}},
	}
	costs := allocateCLITurnCosts(turns, 1.0, nil)
	if math.Abs(costs["a"]-0.25) > 1e-9 || math.Abs(costs["b"]-0.75) > 1e-9 {
		t.Fatalf("costs = %v", costs)
	}
	if got := allocateCLITurnCosts(turns, 0, nil); got != nil {
		t.Fatalf("zero cost should allocate nothing, got %v", got)
	}
}

func TestParseCLIJSONResultCarriesCost(t *testing.T) {
	resp, err := parseJSONResponse([]byte(`{"type":"result","subtype":"success","result":"ok","total_cost_usd":0.12,"num_turns":3,"usage":{"input_tokens":5,"output_tokens":7,"cache_read_input_tokens":900}}`), nil, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage.PromptTokens != 5 || resp.Usage.CacheReadTokens != 0 {
		t.Fatalf("Usage = %+v (must stay input/output only)", resp.Usage)
	}
	pt := resp.ProviderTrace
	if pt == nil || pt.CostUSD != 0.12 || pt.TotalUsage.CacheReadTokens != 900 || pt.NumTurns != 3 {
		t.Fatalf("ProviderTrace = %+v", pt)
	}
}

func TestCLIStreamMsgStringContent(t *testing.T) {
	var ev cliStreamEvent
	line := `{"type":"user","message":{"role":"user","content":"plain prompt"}}`
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		t.Fatalf("string content must parse: %v", err)
	}
	if ev.Message == nil || len(ev.Message.Content) != 0 {
		t.Fatalf("message = %+v", ev.Message)
	}
}

// jsonArrayFixture turns the stream fixture into the single array that
// `--output-format json --verbose` prints (the Chat / non-stream path).
func jsonArrayFixture(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("testdata/claude_cli_stream_tools.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	var events []json.RawMessage
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		events = append(events, json.RawMessage(line))
	}
	out, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "out.json")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func runFakeChat(t *testing.T, ctx context.Context, fixture string) *ChatResponse {
	t.Helper()
	p := NewClaudeCLIProvider(fakeClaudeCLI(t, fixture, 0), WithClaudeCLIWorkDir(t.TempDir()))
	resp, err := p.Chat(ctx, ChatRequest{
		Messages: []Message{{Role: "user", Content: "review"}},
		Options:  map[string]any{OptSessionKey: "trace-test"},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	return resp
}

// The non-stream path (wake / channel runs with Stream=false) must produce the
// same nested trace as ChatStream, and the same response with or without it.
func TestClaudeCLIChatJSONArrayNestedTrace(t *testing.T) {
	fixture := jsonArrayFixture(t)
	plain := runFakeChat(t, context.Background(), fixture)
	chatObs := &recordedObserver{}
	traced := runFakeChat(t, WithNestedTraceObserver(context.Background(), chatObs), fixture)

	if plain.Content != "Review posted: 1 issue." || traced.Content != plain.Content || traced.FinishReason != plain.FinishReason {
		t.Fatalf("content: plain=%q traced=%q", plain.Content, traced.Content)
	}
	if *plain.Usage != *traced.Usage || plain.Usage.PromptTokens != 24 || plain.Usage.CompletionTokens != 400 || plain.Usage.CacheReadTokens != 0 {
		t.Fatalf("Usage plain=%+v traced=%+v", plain.Usage, traced.Usage)
	}
	if plain.ProviderTrace.Nested != (NestedTraceTotals{}) || plain.ProviderTrace.RemainderCostUSD() != 0.5 {
		t.Fatalf("plain ProviderTrace = %+v", plain.ProviderTrace)
	}

	streamObs := &recordedObserver{}
	streamResp, _ := runFakeStream(t, WithNestedTraceObserver(context.Background(), streamObs), "testdata/claude_cli_stream_tools.jsonl")

	type turn struct {
		key, parent string
		usage       Usage
		tools       int
	}
	collect := func(r *recordedObserver) (turns []turn, tools []string) {
		ends := map[string]NestedLLMTurnEnd{}
		for _, e := range r.turnEnds {
			ends[e.Key] = e
		}
		for _, s := range r.turnStarts {
			turns = append(turns, turn{s.Key, s.ParentKey, ends[s.Key].Usage, ends[s.Key].ToolCalls})
		}
		for _, s := range r.toolStarts {
			tools = append(tools, s.Key+"<"+s.ParentKey)
		}
		return turns, tools
	}
	gotTurns, gotTools := collect(chatObs)
	wantTurns, wantTools := collect(streamObs)
	if fmt.Sprint(gotTurns) != fmt.Sprint(wantTurns) || fmt.Sprint(gotTools) != fmt.Sprint(wantTools) {
		t.Fatalf("json path turns=%v tools=%v\nstream path turns=%v tools=%v", gotTurns, gotTools, wantTurns, wantTools)
	}
	if len(chatObs.toolEnds) != 4 || len(chatObs.turnEnds) != 5 {
		t.Fatalf("json path ends: tools %d turns %d", len(chatObs.toolEnds), len(chatObs.turnEnds))
	}
	if fmt.Sprint(chatObs.costs) != fmt.Sprint(streamObs.costs) {
		t.Fatalf("costs json=%v stream=%v", chatObs.costs, streamObs.costs)
	}
	if *traced.ProviderTrace != *streamResp.ProviderTrace {
		t.Fatalf("ProviderTrace json=%+v stream=%+v", traced.ProviderTrace, streamResp.ProviderTrace)
	}
}

// testdata/claude_cli_json_real_2.1.331.json is a real (redacted) capture of
// `claude -p --output-format json --verbose` from the gateway container:
// 3 API messages, 3 tools (one denied), tool results interleaved with blocks
// of the same message, per-event timestamps, partial per-message output.
func TestClaudeCLIRealJSONCapture(t *testing.T) {
	data, err := os.ReadFile("testdata/claude_cli_json_real_2.1.331.json")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 11, 27, 9, 17, 3, 0, time.UTC)
	obs := &recordedObserver{}
	traced, err := parseJSONResponse(data, obs, start)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := parseJSONResponse(data, nil, start)
	if err != nil {
		t.Fatal(err)
	}
	if traced.Content != plain.Content || !strings.HasPrefix(traced.Content, "`/app` could not be listed") {
		t.Fatalf("content traced=%q plain=%q", traced.Content, plain.Content)
	}
	if *traced.Usage != *plain.Usage || traced.Usage.PromptTokens != 6 || traced.Usage.CompletionTokens != 423 {
		t.Fatalf("Usage traced=%+v plain=%+v", traced.Usage, plain.Usage)
	}

	// msg_..9czZ has its second tool_use after the first tool's result:
	// still one turn, not two.
	var keys []string
	for _, s := range obs.turnStarts {
		keys = append(keys, s.Key)
	}
	want := []string{"msg_011ChTBU9czZNKqNk3qh95qk", "msg_011ChTBUGDsFhQk7pUpXpEFm", "msg_011ChTBUP1ebDjZPdGYASGL3"}
	if fmt.Sprint(keys) != fmt.Sprint(want) {
		t.Fatalf("turns = %v, want %v", keys, want)
	}
	ends := map[string]NestedLLMTurnEnd{}
	for _, e := range obs.turnEnds {
		ends[e.Key] = e
	}
	first := ends[want[0]]
	if first.ToolCalls != 2 || first.Usage.CacheCreationTokens != 10729 || first.Usage.CacheReadTokens != 9449 || !first.OutputTokensPartial {
		t.Errorf("first turn = %+v", first)
	}
	if ends[want[1]].ToolCalls != 1 || ends[want[2]].FinishReason != "end_turn" {
		t.Errorf("turns = %+v", ends)
	}

	// Times come from event timestamps.
	if got := obs.turnStarts[0].Start; !got.Equal(start) {
		t.Errorf("first turn start = %v, want process start", got)
	}
	if got := first.End; !got.Equal(time.Date(2026, 11, 27, 9, 17, 5, 283e6, time.UTC)) {
		t.Errorf("first turn end = %v", got)
	}
	if got := obs.turnStarts[1].Start; !got.Equal(time.Date(2026, 11, 27, 9, 17, 5, 335e6, time.UTC)) {
		t.Errorf("second turn start = %v, want last tool result", got)
	}

	if len(obs.toolStarts) != 3 || len(obs.toolEnds) != 3 {
		t.Fatalf("tools = %d/%d", len(obs.toolStarts), len(obs.toolEnds))
	}
	toolEnds := map[string]NestedToolCallEnd{}
	for _, e := range obs.toolEnds {
		toolEnds[e.Key] = e
	}
	if e := toolEnds["toolu_01DmntJJR1j3HyDAsxKTa2RN"]; !e.IsError || !strings.Contains(e.Output, "was blocked") {
		t.Errorf("denied Bash = %+v", e)
	}
	if e := toolEnds["toolu_01UWtsWdPkK8Z6CuUxrxmK7H"]; e.IsError || e.Output != "MISSING" {
		t.Errorf("Bash = %+v", e)
	}
	if obs.toolStarts[2].Name != "Read" || obs.toolStarts[2].Input != `{"file_path":"/etc/hostname"}` {
		t.Errorf("Read start = %+v", obs.toolStarts[2])
	}

	// Totals are the CLI's; children keep input/cache exactly, the parent
	// keeps the output the per-message snapshots miss, and all cost lands on
	// haiku turns.
	pt := traced.ProviderTrace
	if pt.TotalUsage.CacheReadTokens != 51928 || pt.TotalUsage.CacheCreationTokens != 13072 || pt.NumTurns != 4 {
		t.Errorf("TotalUsage = %+v turns %d", pt.TotalUsage, pt.NumTurns)
	}
	rem := pt.RemainderUsage()
	if rem.PromptTokens != 0 || rem.CacheReadTokens != 0 || rem.CacheCreationTokens != 0 || rem.CompletionTokens != 423-25 {
		t.Errorf("remainder = %+v", rem)
	}
	var sum float64
	for _, c := range obs.costs {
		sum += c
	}
	if len(obs.costs) != 3 || math.Abs(sum-pt.CostUSD) > 1e-12 || pt.CostUSD <= 0 || pt.RemainderCostUSD() > 1e-12 {
		t.Errorf("costs = %v total %v remainder %v", obs.costs, pt.CostUSD, pt.RemainderCostUSD())
	}
}
