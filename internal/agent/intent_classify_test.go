package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/providers"
)

// ─── quickClassify ────────────────────────────────────────────────────────

func TestQuickClassify_QuestionMark(t *testing.T) {
	intent, ok := quickClassify("?")
	if !ok || intent != IntentStatusQuery {
		t.Errorf("'?' → got (%v, %v), want (status_query, true)", intent, ok)
	}
}

func TestQuickClassify_CancelKeywords(t *testing.T) {
	cases := []string{"stop", "cancel", "abort", "thôi", "dừng", "hủy", "nevermind"}
	for _, kw := range cases {
		t.Run(kw, func(t *testing.T) {
			intent, ok := quickClassify(kw)
			if !ok || intent != IntentCancel {
				t.Errorf("%q → got (%v, %v), want (cancel, true)", kw, intent, ok)
			}
		})
	}
}

func TestQuickClassify_LongMessage_FallsThrough(t *testing.T) {
	// > 15 runes → should not fast-path
	msg := "this is a much longer message that exceeds fifteen runes"
	_, ok := quickClassify(msg)
	if ok {
		t.Error("long message should not be fast-pathed")
	}
}

func TestQuickClassify_CancelEmbeddedInLongerWord_NoMatch(t *testing.T) {
	// "nonstop" contains "stop" but not as a whole word; length is also short.
	_, ok := quickClassify("nonstop")
	if ok {
		t.Error("'nonstop' should not match cancel (stop is not a whole word)")
	}
}

func TestQuickClassify_MixedCase(t *testing.T) {
	intent, ok := quickClassify("STOP")
	if !ok || intent != IntentCancel {
		t.Errorf("'STOP' → got (%v, %v), want (cancel, true)", intent, ok)
	}
}

func TestQuickClassify_EmptyString_FallsThrough(t *testing.T) {
	_, ok := quickClassify("")
	if ok {
		t.Error("empty string should fall through to LLM")
	}
}

// ─── containsWholeWord ───────────────────────────────────────────────────

func TestContainsWholeWord(t *testing.T) {
	cases := []struct {
		s, kw string
		want  bool
	}{
		{"stop", "stop", true},
		{"please stop now", "stop", true},
		{"nonstop", "stop", false},
		{"stopping", "stop", false},
		{"stop!", "stop", true},
		{"hủy bỏ", "hủy", true},
		{"no match here", "cancel", false},
		{"cancel me", "cancel", true},
	}
	for _, c := range cases {
		got := containsWholeWord(c.s, c.kw)
		if got != c.want {
			t.Errorf("containsWholeWord(%q, %q) = %v, want %v", c.s, c.kw, got, c.want)
		}
	}
}

// ─── ClassifyIntent via mock provider ────────────────────────────────────

// stubProvider implements providers.Provider for intent classification tests.
type stubProvider struct {
	response string
	err      error
}

func (s *stubProvider) Chat(_ context.Context, _ providers.ChatRequest) (*providers.ChatResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &providers.ChatResponse{Content: s.response}, nil
}

func (s *stubProvider) ChatStream(_ context.Context, _ providers.ChatRequest, _ func(providers.StreamChunk)) (*providers.ChatResponse, error) {
	return &providers.ChatResponse{Content: s.response}, nil
}

func (s *stubProvider) DefaultModel() string { return "stub-model" }
func (s *stubProvider) Name() string         { return "stub" }

func TestClassifyIntent_FastPath_Cancel(t *testing.T) {
	p := &stubProvider{response: "new_task"} // LLM would say new_task, but fast-path wins
	intent := ClassifyIntent(context.Background(), p, "gpt-4o", "stop")
	if intent != IntentCancel {
		t.Errorf("got %q, want cancel", intent)
	}
}

func TestClassifyIntent_FastPath_StatusQuery(t *testing.T) {
	p := &stubProvider{response: "new_task"}
	intent := ClassifyIntent(context.Background(), p, "gpt-4o", "?")
	if intent != IntentStatusQuery {
		t.Errorf("got %q, want status_query", intent)
	}
}

func TestClassifyIntent_LLM_StatusQuery(t *testing.T) {
	p := &stubProvider{response: "status_query"}
	intent := ClassifyIntent(context.Background(), p, "gpt-4o", "what are you currently working on?")
	if intent != IntentStatusQuery {
		t.Errorf("got %q, want status_query", intent)
	}
}

func TestClassifyIntent_LLM_Cancel(t *testing.T) {
	p := &stubProvider{response: "cancel"}
	intent := ClassifyIntent(context.Background(), p, "gpt-4o", "please stop what you are doing right now")
	if intent != IntentCancel {
		t.Errorf("got %q, want cancel", intent)
	}
}

func TestClassifyIntent_LLM_Steer(t *testing.T) {
	p := &stubProvider{response: "steer"}
	intent := ClassifyIntent(context.Background(), p, "gpt-4o", "also check the error logs while you are at it")
	if intent != IntentSteer {
		t.Errorf("got %q, want steer", intent)
	}
}

func TestClassifyIntent_LLM_NewTask(t *testing.T) {
	p := &stubProvider{response: "new_task"}
	intent := ClassifyIntent(context.Background(), p, "gpt-4o", "what is the capital of France?")
	if intent != IntentNewTask {
		t.Errorf("got %q, want new_task", intent)
	}
}

func TestClassifyIntent_LLM_Error_FallsBackToNewTask(t *testing.T) {
	p := &stubProvider{err: errors.New("provider unavailable")}
	intent := ClassifyIntent(context.Background(), p, "gpt-4o", "some long message that exceeds 15 runes")
	if intent != IntentNewTask {
		t.Errorf("on error should return new_task, got %q", intent)
	}
}

type stubDecisionProvider struct {
	intent     string
	confidence float64
	err        error
}

func (s *stubDecisionProvider) Evaluate(_ context.Context, _ providers.DecisionRequest) (*providers.DecisionResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	conf := 0.95
	if s.confidence > 0 {
		conf = s.confidence
	}
	return &providers.DecisionResponse{
		Answers: map[string]any{
			"intent": providers.ChoiceAnswer{
				Choice:     s.intent,
				Selected:   s.intent,
				Confidence: conf,
			},
		},
	}, nil
}
func (s *stubDecisionProvider) Name() string          { return "stub-decision" }
func (s *stubDecisionProvider) CanonicalName() string { return "stub-decision" }
func (s *stubDecisionProvider) DefaultModel() string  { return "stub-model" }

func TestClassifyIntent_WithDecisionProvider(t *testing.T) {
	// LLM would say new_task, but DecisionProvider says status_query
	llm := &stubProvider{response: "new_task"}
	decision := &stubDecisionProvider{intent: "status_query"}

	intent := ClassifyIntentWithUsageCapsAndDecision(context.Background(), decision, nil, llm, "gpt-4o", "how is the background process going right now?")
	if intent != IntentStatusQuery {
		t.Errorf("expected DecisionProvider to win with status_query, got %q", intent)
	}

	// Decision provider returns low confidence (e.g. 0.40) -> should fall through to LLM
	lowConfDecision := &stubDecisionProvider{intent: "cancel", confidence: 0.40}
	intent = ClassifyIntentWithUsageCapsAndDecision(context.Background(), lowConfDecision, nil, llm, "gpt-4o", "how is the background process going right now?")
	if intent != IntentNewTask {
		t.Errorf("expected low confidence to fall through to LLM (new_task), got %q", intent)
	}

	// Decision provider returns "unknown" -> should fall through to LLM
	unknownDecision := &stubDecisionProvider{intent: "unknown", confidence: 0.80}
	intent = ClassifyIntentWithUsageCapsAndDecision(context.Background(), unknownDecision, nil, llm, "gpt-4o", "how is the background process going right now?")
	if intent != IntentNewTask {
		t.Errorf("expected unknown choice to fall through to LLM (new_task), got %q", intent)
	}

	// Decision provider returns error -> should fall through to LLM
	errDecision := &stubDecisionProvider{err: errors.New("decision provider down")}
	intent = ClassifyIntentWithUsageCapsAndDecision(context.Background(), errDecision, nil, llm, "gpt-4o", "how is the background process going right now?")
	if intent != IntentNewTask {
		t.Errorf("expected LLM fallback to return new_task, got %q", intent)
	}
}

func TestClassifyIntent_ContextCanceled_NoFallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // canceled upfront

	llmCalled := false
	llm := &stubProviderFunc{fn: func(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
		llmCalled = true
		return &providers.ChatResponse{Content: "status_query"}, nil
	}}
	slowDecision := &stubDecisionProvider{err: context.Canceled}

	intent := ClassifyIntentWithUsageCapsAndDecision(ctx, slowDecision, nil, llm, "gpt-4o", "how is the background process going right now?")
	if intent != IntentNewTask {
		t.Errorf("expected IntentNewTask on canceled context, got %v", intent)
	}
	if llmCalled {
		t.Errorf("expected LLM chat fallback NOT to be called when context is canceled")
	}
}

func TestClassifyIntent_DecisionSubTimeout_FallsBackToChat(t *testing.T) {
	ctx := context.Background()

	llmCalled := false
	llm := &stubProviderFunc{fn: func(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
		llmCalled = true
		return &providers.ChatResponse{Content: "status_query"}, nil
	}}
	timeoutDecision := &stubDecisionProvider{err: context.DeadlineExceeded}

	intent := ClassifyIntentWithUsageCapsAndDecision(ctx, timeoutDecision, nil, llm, "gpt-4o", "how is the background process going right now?")
	if !llmCalled {
		t.Errorf("expected LLM chat fallback to be called on decision sub-timeout")
	}
	if intent != IntentStatusQuery {
		t.Errorf("expected IntentStatusQuery from chat fallback, got %v", intent)
	}
}

type stubProviderFunc struct {
	fn func(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error)
}

func (s *stubProviderFunc) Chat(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
	if s.fn != nil {
		return s.fn(ctx, req)
	}
	return &providers.ChatResponse{Content: "new_task"}, nil
}
func (s *stubProviderFunc) ChatStream(ctx context.Context, req providers.ChatRequest, _ func(providers.StreamChunk)) (*providers.ChatResponse, error) {
	return s.Chat(ctx, req)
}
func (s *stubProviderFunc) DefaultModel() string { return "gpt-4o" }
func (s *stubProviderFunc) Name() string         { return "stub-func" }

func BenchmarkIntentClassify_FastPathKeyword(b *testing.B) {
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ClassifyIntent(ctx, nil, "gpt-4o", "stop")
	}
}

func BenchmarkIntentClassify_DecisionProvider(b *testing.B) {
	ctx := context.Background()
	decision := &stubDecisionProvider{intent: "status_query"}
	msg := "what are you currently doing right now?"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ClassifyIntentWithUsageCapsAndDecision(ctx, decision, nil, nil, "gpt-4o", msg)
	}
}

func BenchmarkIntentClassify_GenerativeLLM(b *testing.B) {
	ctx := context.Background()
	llm := &stubProvider{response: "status_query"}
	msg := "what are you currently doing right now?"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ClassifyIntent(ctx, llm, "gpt-4o", msg)
	}
}

// ─── formatPhase / formatToolLabel ───────────────────────────────────────

func TestFormatPhase_KnownPhases(t *testing.T) {
	cases := []struct {
		phase, tool, locale string
		wantEmpty           bool
	}{
		{"thinking", "", "en", false},
		{"tool_exec", "exec", "en", false},
		{"tool_exec", "", "en", false},
		{"compacting", "", "en", false},
		{"unknown_phase", "", "en", false},
	}
	for _, c := range cases {
		got := formatPhase(c.phase, c.tool, c.locale)
		if c.wantEmpty && got != "" {
			t.Errorf("formatPhase(%q,%q) = %q, want empty", c.phase, c.tool, got)
		}
		if !c.wantEmpty && got == "" {
			t.Errorf("formatPhase(%q,%q) returned empty", c.phase, c.tool)
		}
	}
}

func TestFormatToolLabel(t *testing.T) {
	cases := []struct {
		tool string
		want string
	}{
		{"web_search", "web search"},
		{"web_fetch", "web search"},
		{"exec", "code execution"},
		{"browser", "browser"},
		{"spawn", "delegation"},
		{"memory_read", "memory"},
		{"file_write", "file operations"},
		{"custom_tool", "custom_tool"},
	}
	for _, c := range cases {
		got := formatToolLabel(c.tool)
		if got != c.want {
			t.Errorf("formatToolLabel(%q) = %q, want %q", c.tool, got, c.want)
		}
	}
}

// ─── FormatStatusReply ────────────────────────────────────────────────────

func TestFormatStatusReply_NilStatus(t *testing.T) {
	got := FormatStatusReply(nil, "en")
	if got == "" {
		t.Error("expected non-empty reply for nil status")
	}
}

func TestFormatStatusReply_WithStatus(t *testing.T) {
	status := &AgentActivityStatus{
		Phase:     "thinking",
		Iteration: 2,
	}
	got := FormatStatusReply(status, "en")
	if got == "" {
		t.Error("expected non-empty reply with status")
	}
}

// ─── IsExactCancelKeyword ─────────────────────────────────────────────────

func TestIsExactCancelKeyword_ExactMatches(t *testing.T) {
	cases := []string{"stop", "cancel", "abort", "thôi", "dừng", "hủy", "取消", "停", "nevermind", "never mind"}
	for _, kw := range cases {
		t.Run(kw, func(t *testing.T) {
			if !IsExactCancelKeyword(kw) {
				t.Errorf("%q should be recognized as cancel keyword", kw)
			}
		})
	}
}

func TestIsExactCancelKeyword_CaseInsensitive(t *testing.T) {
	cases := []string{"STOP", "Stop", "CANCEL", "Cancel", "ABORT", "Abort"}
	for _, kw := range cases {
		t.Run(kw, func(t *testing.T) {
			if !IsExactCancelKeyword(kw) {
				t.Errorf("%q should be recognized (case-insensitive)", kw)
			}
		})
	}
}

func TestIsExactCancelKeyword_WithWhitespace(t *testing.T) {
	cases := []string{"  stop  ", "\tstop\n", " cancel "}
	for _, kw := range cases {
		t.Run(kw, func(t *testing.T) {
			if !IsExactCancelKeyword(kw) {
				t.Errorf("%q should be recognized after trimming", kw)
			}
		})
	}
}

func TestIsExactCancelKeyword_NonMatches(t *testing.T) {
	cases := []string{
		"stop now",          // not exact
		"please stop",       // not exact
		"nonstop",           // embedded
		"stop it",           // not exact
		"cancel the order",  // not exact
		"don't stop",        // not exact
		"",                  // empty
		"hello",             // unrelated
		"làm đơn giản thôi", // contains "thôi" but not exact
	}
	for _, msg := range cases {
		t.Run(msg, func(t *testing.T) {
			if IsExactCancelKeyword(msg) {
				t.Errorf("%q should NOT be recognized as cancel keyword", msg)
			}
		})
	}
}

func TestClassifyIntentWithDecisionResolverSkippedOnKeywordHit(t *testing.T) {
	called := false
	resolve := func(context.Context) providers.DecisionProvider { called = true; return nil }
	if got := ClassifyIntentWithDecisionResolver(context.Background(), resolve, nil, nil, "", "stop"); got != IntentCancel {
		t.Fatalf("expected cancel via keyword, got %q", got)
	}
	if called {
		t.Fatal("resolver must not run when the keyword fast-path matches")
	}
}
