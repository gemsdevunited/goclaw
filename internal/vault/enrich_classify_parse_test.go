package vault

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/providers"
	usagecaps "github.com/nextlevelbuilder/goclaw/internal/usage/caps"
)

// ============================================================================
// JSON Parsing Tests
// ============================================================================

// TestParseClassifyResponse_Valid parses standard JSON array.
func TestParseClassifyResponse_Valid(t *testing.T) {
	raw := `[
		{"idx":1,"type":"reference","ctx":"mentions config"},
		{"idx":2,"type":"depends_on","ctx":"requires authentication"}
	]`

	results, err := parseClassifyResponse(raw, 2)
	if err != nil {
		t.Fatalf("parseClassifyResponse failed: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("Expected 2 results, got %d", len(results))
	}

	if results[0].Idx != 1 || results[0].Type != "reference" {
		t.Errorf("Result 0: got idx=%d type=%q, want idx=1 type=reference", results[0].Idx, results[0].Type)
	}

	if results[1].Idx != 2 || results[1].Type != "depends_on" {
		t.Errorf("Result 1: got idx=%d type=%q, want idx=2 type=depends_on", results[1].Idx, results[1].Type)
	}
}

// TestParseClassifyResponse_WithCodeFence strips ```json fences.
func TestParseClassifyResponse_WithCodeFence(t *testing.T) {
	raw := "```json\n[{\"idx\":1,\"type\":\"reference\",\"ctx\":\"test\"}]\n```"

	results, err := parseClassifyResponse(raw, 1)
	if err != nil {
		t.Fatalf("parseClassifyResponse with code fence failed: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}

	if results[0].Type != "reference" {
		t.Errorf("Expected type reference, got %q", results[0].Type)
	}
}

// TestParseClassifyResponse_InvalidJSON returns error on unmarshal failure.
func TestParseClassifyResponse_InvalidJSON(t *testing.T) {
	raw := `{invalid json}`

	_, err := parseClassifyResponse(raw, 1)
	if err == nil {
		t.Fatalf("parseClassifyResponse should return error for invalid JSON")
	}

	if !strings.Contains(err.Error(), "unmarshal") {
		t.Errorf("Error should mention unmarshal: %v", err)
	}
}

// TestParseClassifyResponse_OutOfRangeIdx filters silently, keeps valid entries.
func TestParseClassifyResponse_OutOfRangeIdx(t *testing.T) {
	raw := `[
		{"idx":1,"type":"reference","ctx":"ok"},
		{"idx":99,"type":"related","ctx":"invalid idx"},
		{"idx":2,"type":"extends","ctx":"ok"}
	]`

	results, err := parseClassifyResponse(raw, 2)
	if err != nil {
		t.Fatalf("parseClassifyResponse failed: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("Expected 2 results (out-of-range filtered), got %d", len(results))
	}

	if results[0].Idx != 1 || results[1].Idx != 2 {
		t.Errorf("Out-of-range entry not filtered properly")
	}
}

// TestParseClassifyResponse_UnknownType filters silently.
func TestParseClassifyResponse_UnknownType(t *testing.T) {
	raw := `[
		{"idx":1,"type":"reference","ctx":"ok"},
		{"idx":2,"type":"unknown_type","ctx":"invalid"},
		{"idx":3,"type":"related","ctx":"ok"}
	]`

	results, err := parseClassifyResponse(raw, 3)
	if err != nil {
		t.Fatalf("parseClassifyResponse failed: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("Expected 2 results (unknown type filtered), got %d", len(results))
	}

	if results[0].Type != "reference" || results[1].Type != "related" {
		t.Errorf("Unknown type not filtered properly")
	}
}

// TestParseClassifyResponse_SKIPType preserves SKIP in output.
func TestParseClassifyResponse_SKIPType(t *testing.T) {
	raw := `[
		{"idx":1,"type":"reference","ctx":"valid"},
		{"idx":2,"type":"SKIP","ctx":"no relationship"}
	]`

	results, err := parseClassifyResponse(raw, 2)
	if err != nil {
		t.Fatalf("parseClassifyResponse failed: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("Expected 2 results including SKIP, got %d", len(results))
	}

	skipFound := false
	for _, r := range results {
		if r.Type == "SKIP" {
			skipFound = true
			break
		}
	}

	if !skipFound {
		t.Errorf("SKIP type should be preserved in results")
	}
}

// TestParseClassifyResponse_EmptyArray returns valid empty slice.
func TestParseClassifyResponse_EmptyArray(t *testing.T) {
	raw := `[]`

	results, err := parseClassifyResponse(raw, 5)
	if err != nil {
		t.Fatalf("parseClassifyResponse failed on empty array: %v", err)
	}

	if len(results) != 0 {
		t.Fatalf("Expected 0 results, got %d", len(results))
	}
}

// TestParseClassifyResponse_CtxTruncation truncates context over 256 chars.
func TestParseClassifyResponse_CtxTruncation(t *testing.T) {
	longCtx := strings.Repeat("x", 300)
	raw := fmt.Sprintf(`[{"idx":1,"type":"reference","ctx":"%s"}]`, longCtx)

	results, err := parseClassifyResponse(raw, 1)
	if err != nil {
		t.Fatalf("parseClassifyResponse failed: %v", err)
	}

	if len([]rune(results[0].Ctx)) > classifyCtxMaxLen {
		t.Errorf("Context not truncated: got %d runes, want ≤%d",
			len([]rune(results[0].Ctx)), classifyCtxMaxLen)
	}
}

// TestParseClassifyResponse_ZeroIdx filters silently (idx must be >= 1).
func TestParseClassifyResponse_ZeroIdx(t *testing.T) {
	raw := `[
		{"idx":0,"type":"reference","ctx":"invalid"},
		{"idx":1,"type":"reference","ctx":"valid"}
	]`

	results, err := parseClassifyResponse(raw, 1)
	if err != nil {
		t.Fatalf("parseClassifyResponse failed: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("Expected 1 result (idx 0 filtered), got %d", len(results))
	}

	if results[0].Idx != 1 {
		t.Errorf("Zero idx should be filtered")
	}
}

type mockDecisionProvider struct {
	answers map[string]any
	err     error
}

func (m *mockDecisionProvider) Evaluate(_ context.Context, _ providers.DecisionRequest) (*providers.DecisionResponse, error) {
	if m.err != nil {
		return nil, m.err
	}
	return &providers.DecisionResponse{Answers: m.answers}, nil
}
func (m *mockDecisionProvider) Name() string          { return "mock-decision" }
func (m *mockDecisionProvider) CanonicalName() string { return "mock-decision" }
func (m *mockDecisionProvider) DefaultModel() string  { return "mock-model" }

func TestClassifyCandidatesWithDecision(t *testing.T) {
	source := classifyDoc{
		DocID:   "doc-source",
		Title:   "API Auth Spec",
		Summary: "Explains bearer tokens",
	}
	candidates := []classifyDoc{
		{DocID: "cand-1", Title: "OAuth Gateway", Summary: "Issues tokens"},
		{DocID: "cand-2", Title: "Payment Service", Summary: "Handles billing"},
	}

	// Documented API format uses "Choice"
	dp := &mockDecisionProvider{
		answers: map[string]any{
			"cand_1": providers.ChoiceAnswer{Choice: "depends_on", Confidence: 0.95},
			"cand_2": providers.ChoiceAnswer{Choice: "SKIP", Confidence: 0.88},
		},
	}

	results, ok := classifyCandidatesWithDecision(context.Background(), dp, source, candidates)
	if !ok {
		t.Fatal("expected classifyCandidatesWithDecision to succeed")
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Idx != 1 || results[0].Type != "depends_on" {
		t.Errorf("unexpected result 0: %+v", results[0])
	}
	if results[0].Ctx != "requires OAuth Gateway to function" {
		t.Errorf("unexpected result 0 Ctx: %q, want 'requires OAuth Gateway to function'", results[0].Ctx)
	}
	if results[1].Idx != 2 || results[1].Type != "SKIP" {
		t.Errorf("unexpected result 1: %+v", results[1])
	}

	// Missing / empty answers path -> must return ok=false so caller triggers LLM fallback
	emptyDp := &mockDecisionProvider{
		answers: map[string]any{},
	}
	_, ok = classifyCandidatesWithDecision(context.Background(), emptyDp, source, candidates)
	if ok {
		t.Fatal("expected empty answers to return ok=false for LLM fallback")
	}

	// Incomplete answers path (only 1 out of 2 candidates answered) -> must return ok=false
	partialDp := &mockDecisionProvider{
		answers: map[string]any{
			"cand_1": providers.ChoiceAnswer{Choice: "depends_on", Confidence: 0.95},
			// cand_2 missing
		},
	}
	_, ok = classifyCandidatesWithDecision(context.Background(), partialDp, source, candidates)
	if ok {
		t.Fatal("expected incomplete candidate answers to return ok=false for LLM fallback")
	}

	// Unknown / invalid values path -> must return ok=false
	unknownDp := &mockDecisionProvider{
		answers: map[string]any{
			"cand_1": providers.ChoiceAnswer{Choice: "unknown", Confidence: 0.0},
			"cand_2": providers.ChoiceAnswer{Choice: "", Confidence: 0.0},
		},
	}
	_, ok = classifyCandidatesWithDecision(context.Background(), unknownDp, source, candidates)
	if ok {
		t.Fatal("expected unknown/empty choices to return ok=false for LLM fallback")
	}

	// Bogus / invalid relationship label path -> must return ok=false (must NOT accept bogus types like "bogus_type")
	bogusDp := &mockDecisionProvider{
		answers: map[string]any{
			"cand_1": providers.ChoiceAnswer{Choice: "depends_on", Confidence: 0.95},
			"cand_2": providers.ChoiceAnswer{Choice: "bogus_type", Confidence: 0.88},
		},
	}
	_, ok = classifyCandidatesWithDecision(context.Background(), bogusDp, source, candidates)
	if ok {
		t.Fatal("expected bogus relationship label to return ok=false for LLM fallback")
	}

	// Low confidence path (< 0.60) -> must return ok=false to trigger LLM fallback
	lowConfDp := &mockDecisionProvider{
		answers: map[string]any{
			"cand_1": providers.ChoiceAnswer{Choice: "depends_on", Confidence: 0.40},
			"cand_2": providers.ChoiceAnswer{Choice: "SKIP", Confidence: 0.88},
		},
	}
	_, ok = classifyCandidatesWithDecision(context.Background(), lowConfDp, source, candidates)
	if ok {
		t.Fatal("expected low confidence (< 0.60) to return ok=false for LLM fallback")
	}

	// Error path
	errDp := &mockDecisionProvider{err: fmt.Errorf("network error")}
	_, ok = classifyCandidatesWithDecision(context.Background(), errDp, source, candidates)
	if ok {
		t.Fatal("expected error to return ok=false")
	}
}

func TestClassifyCandidatesWithDecision_CapAndCancellationErrors(t *testing.T) {
	source := classifyDoc{
		DocID:   "doc-source",
		Title:   "Source Document",
		Path:    "docs/source.md",
		Summary: "Architecture design doc.",
	}
	candidates := []classifyDoc{
		{DocID: "doc-1", Title: "DB Design", Path: "docs/db.md", Summary: "Database schema."},
	}

	// ErrCapExceeded must be returned as ErrCapExceeded directly, not masked as ErrDecisionFallback
	capErrDp := &mockDecisionProvider{err: usagecaps.ErrCapExceeded}
	_, err := classifyCandidatesWithDecisionAndCaps(context.Background(), capErrDp, nil, source, candidates)
	if !errors.Is(err, usagecaps.ErrCapExceeded) {
		t.Fatalf("expected ErrCapExceeded, got %v", err)
	}

	// context.Canceled must be returned as context.Canceled directly
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = classifyCandidatesWithDecisionAndCaps(canceledCtx, &mockDecisionProvider{}, nil, source, candidates)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	// Sub-timeout (context.DeadlineExceeded) when parent context is active must wrap ErrDecisionFallback to trigger chat fallback
	timeoutDp := &mockDecisionProvider{err: context.DeadlineExceeded}
	_, err = classifyCandidatesWithDecisionAndCaps(context.Background(), timeoutDp, nil, source, candidates)
	if !errors.Is(err, ErrDecisionFallback) {
		t.Fatalf("expected ErrDecisionFallback for sub-timeout when parent context is active, got %v", err)
	}

	// Network / provider error must wrap ErrDecisionFallback
	netErrDp := &mockDecisionProvider{err: fmt.Errorf("upstream timeout")}
	_, err = classifyCandidatesWithDecisionAndCaps(context.Background(), netErrDp, nil, source, candidates)
	if !errors.Is(err, ErrDecisionFallback) {
		t.Fatalf("expected ErrDecisionFallback for network error, got %v", err)
	}
}
