package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestDecision_Validation(t *testing.T) {
	// Empty state
	err := ValidateDecisionRequest(DecisionRequest{
		Questions: map[string]DecisionQuestion{
			"test": {Type: QuestionTypeNoul},
		},
	})
	if err == nil {
		t.Fatal("expected error for empty state")
	}

	// Empty questions
	err = ValidateDecisionRequest(DecisionRequest{
		State: "some state",
	})
	if err == nil {
		t.Fatal("expected error for empty questions")
	}

	// Invalid question type
	err = ValidateDecisionRequest(DecisionRequest{
		State: "some state",
		Questions: map[string]DecisionQuestion{
			"test": {Type: "invalid_type"},
		},
	})
	if err == nil {
		t.Fatal("expected error for invalid question type")
	}

	// Valid request
	err = ValidateDecisionRequest(DecisionRequest{
		State: "some state",
		Questions: map[string]DecisionQuestion{
			"test": {Type: QuestionTypeChoice, Instructions: "pick one"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error for valid request: %v", err)
	}
}

func TestDecision_ResponseHelpers(t *testing.T) {
	resp := &DecisionResponse{
		Answers: map[string]any{
			"intent": ChoiceAnswer{
				Selected:   "bug_fix",
				Confidence: 0.95,
				Probabilities: map[string]float64{
					"bug_fix": 0.95,
					"feature": 0.05,
				},
			},
			"risk": ScoreAnswer{
				Score:      0.25,
				Confidence: 0.9,
			},
			"urgent": NoulAnswer{
				Probability: 0.88,
			},
		},
	}

	// GetChoice
	ca, ok := resp.GetChoice("intent")
	if !ok || ca == nil {
		t.Fatal("expected to get choice answer")
	}
	if ca.Selected != "bug_fix" || ca.Confidence != 0.95 {
		t.Fatalf("unexpected choice answer: %+v", ca)
	}

	// GetScore
	score, ok := resp.GetScore("risk")
	if !ok || score != 0.25 {
		t.Fatalf("unexpected score: %f", score)
	}

	// GetNoul
	prob, ok := resp.GetNoul("urgent")
	if !ok || prob != 0.88 {
		t.Fatalf("unexpected noul prob: %f", prob)
	}

	// Missing key
	if _, ok := resp.GetChoice("missing"); ok {
		t.Fatal("expected false for missing key")
	}
}

func TestCloudflareClefProvider_Evaluate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check headers
		if r.Header.Get("Authorization") != "Bearer test-cf-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "bad content type", http.StatusBadRequest)
			return
		}

		// Cloudflare Workers AI URL must contain full model ID
		if !strings.HasSuffix(r.URL.Path, "/@cf/cloudflare/clef-flash") {
			http.Error(w, fmt.Sprintf("expected URL path with full model ID, got %s", r.URL.Path), http.StatusBadRequest)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read error", http.StatusBadRequest)
			return
		}

		var req cfClefRequest
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "json error", http.StatusBadRequest)
			return
		}

		// Cloudflare Workers AI request body requires short selector (e.g. "clef-flash")
		if req.Model != "clef-flash" {
			http.Error(w, fmt.Sprintf("expected body model selector 'clef-flash', got '%s'", req.Model), http.StatusBadRequest)
			return
		}

		if req.State != "User is asking: what is the current task?" {
			http.Error(w, "unexpected state", http.StatusBadRequest)
			return
		}

		// Cloudflare Workers AI documented envelope uses "choice" and "noul"
		resp := cfClefEnvelope{
			Success: true,
		}
		resp.Result.Answers = map[string]json.RawMessage{
			"intent": json.RawMessage(`{"choice":"status_query","confidence":0.96,"probabilities":{"status_query":0.96,"cancel":0.02,"steer":0.02}}`),
			"urgent": json.RawMessage(`{"type":"noul","noul":0.15}`),
			"impact": json.RawMessage(`{"score":1.2}`),
		}
		resp.Result.Usage = &struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		}{
			PromptTokens:     42,
			CompletionTokens: 0,
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	provider := NewCloudflareClefProvider("test-account", "test-cf-token", ClefFlashModel).
		WithBaseURL(server.URL)

	req := DecisionRequest{
		State: "User is asking: what is the current task?",
		Questions: map[string]DecisionQuestion{
			"intent": {
				Type:         QuestionTypeChoice,
				Instructions: "Classify intent",
				Criteria: map[string]string{
					"status_query": "asking about progress",
					"cancel":       "abort",
				},
			},
			"urgent": {
				Type:         QuestionTypeNoul,
				Instructions: "Is this urgent?",
			},
			"impact": {
				Type:         QuestionTypeScore,
				Instructions: "Impact scale",
			},
		},
	}

	resp, err := provider.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatalf("Clef Evaluate failed: %v", err)
	}

	if resp.Model != ClefFlashModel {
		t.Fatalf("expected model %s, got %s", ClefFlashModel, resp.Model)
	}
	if resp.Usage == nil || resp.Usage.PromptTokens != 42 || resp.Usage.CompletionTokens != 0 {
		t.Fatalf("unexpected usage: %+v", resp.Usage)
	}

	ca, ok := resp.GetChoice("intent")
	if !ok || ca.Value() != "status_query" || ca.Confidence != 0.96 {
		t.Fatalf("unexpected choice: %+v", ca)
	}

	prob, ok := resp.GetNoul("urgent")
	if !ok || prob != 0.15 {
		t.Fatalf("unexpected noul: %f", prob)
	}

	score, ok := resp.GetScore("impact")
	if !ok || score != 1.2 {
		t.Fatalf("unexpected score: %f", score)
	}
}

func TestClefProvider_BaseURLNormalization(t *testing.T) {
	var requestedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"result": {"urgent": {"probability": 0.5}}, "usage": {"prompt_tokens": 10, "completion_tokens": 0}}`)
	}))
	defer server.Close()

	// BaseURL has redundant account ID and /ai/run suffix
	clef := NewCloudflareClefProvider("acc123", "dummy-token", ClefFlashModel).
		WithBaseURL(server.URL + "/client/v4/accounts/acc123/ai/run")

	req := DecisionRequest{
		State: "test state",
		Questions: map[string]DecisionQuestion{
			"urgent": {Type: QuestionTypeNoul, Instructions: "is urgent?"},
		},
	}
	if _, err := clef.Evaluate(context.Background(), req); err != nil {
		t.Fatalf("Evaluate failed: %v", err)
	}

	expectedPath := "/client/v4/accounts/acc123/ai/run/@cf/cloudflare/clef-flash"
	if requestedPath != expectedPath {
		t.Fatalf("requestedPath = %q, want %q (no duplicate acc123)", requestedPath, expectedPath)
	}
}

func TestTypeSafeAIJevProvider_Evaluate(t *testing.T) {
	// 1. Native TypeSafe AI test (endpoint /v1/systemone, question type "noul", model "jev-latest")
	nativeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-jev-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/v1/systemone") {
			http.Error(w, fmt.Sprintf("expected native /v1/systemone endpoint, got %s", r.URL.Path), http.StatusBadRequest)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read error", http.StatusBadRequest)
			return
		}

		var req jevRequest
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "json error", http.StatusBadRequest)
			return
		}

		// Model must be native TypeSafe alias (jev-latest)
		if req.Model != "jev-latest" {
			http.Error(w, fmt.Sprintf("expected model 'jev-latest', got '%s'", req.Model), http.StatusBadRequest)
			return
		}

		// Native TypeSafe AI uses "noul" primitive type
		if q, ok := req.Questions["is_blocking"]; !ok || q.Type != "noul" {
			http.Error(w, fmt.Sprintf("expected question type 'noul' for is_blocking in native mode, got '%s'", q.Type), http.StatusBadRequest)
			return
		}

		// Choice criteria must be normalized to an object
		if q, ok := req.Questions["intent"]; !ok || q.Criteria == nil {
			http.Error(w, "missing intent question or criteria", http.StatusBadRequest)
			return
		} else if _, isMap := q.Criteria.(map[string]any); !isMap {
			http.Error(w, fmt.Sprintf("expected choice criteria to be an object, got %T", q.Criteria), http.StatusBadRequest)
			return
		}

		// Score criteria must be an array of levels
		if q, ok := req.Questions["risk"]; !ok || q.Criteria == nil {
			http.Error(w, "missing risk question or criteria", http.StatusBadRequest)
			return
		} else if _, isArr := q.Criteria.([]any); !isArr {
			http.Error(w, fmt.Sprintf("expected score criteria to be an array, got %T", q.Criteria), http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{
			"model": "jev-1.14.0",
			"usage": {
				"prompt_tokens": 42,
				"completion_tokens": 0,
				"total_tokens": 42
			},
			"answers": {
				"intent": { "choice": "cancel", "confidence": 0.94 },
				"risk": { "score": 0.85, "confidence": 0.88 },
				"is_blocking": { "noul": 0.95 }
			}
		}`)
	}))
	defer nativeServer.Close()

	provider := NewTypeSafeAIJevProvider("test-jev-key", nativeServer.URL, JevModel)

	req := DecisionRequest{
		State: "Stop this task right now!",
		Questions: map[string]DecisionQuestion{
			"intent": {
				Type:         QuestionTypeChoice,
				Instructions: "Classify intent",
				Criteria:     []string{"status_query", "cancel", "steer"},
			},
			"risk": {
				Type:         QuestionTypeScore,
				Instructions: "Evaluate risk",
			},
			"is_blocking": {
				Type:         QuestionTypeNoul,
				Instructions: "Is this blocking?",
			},
		},
	}

	resp, err := provider.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatalf("Native Jev Evaluate failed: %v", err)
	}

	// Verify served model and token usage
	if resp.Model != "jev-1.14.0" {
		t.Fatalf("expected served model 'jev-1.14.0', got '%s'", resp.Model)
	}
	if resp.Usage == nil || resp.Usage.PromptTokens != 42 || resp.Usage.TotalTokens != 42 {
		t.Fatalf("unexpected usage: %+v", resp.Usage)
	}

	ca, ok := resp.GetChoice("intent")
	if !ok || ca.Value() != "cancel" || ca.Confidence != 0.94 {
		t.Fatalf("unexpected choice: %+v", ca)
	}

	score, ok := resp.GetScore("risk")
	if !ok || score != 0.85 {
		t.Fatalf("unexpected score: %f", score)
	}

	prob, ok := resp.GetNoul("is_blocking")
	if !ok || prob != 0.95 {
		t.Fatalf("unexpected noul prob: %f", prob)
	}

	// 2. Vercel AI Gateway adaptive mode (endpoint /v1/evaluate, question type "boolean", model "typesafe-ai/jev")
	vercelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/v1/evaluate") {
			http.Error(w, fmt.Sprintf("expected vercel /v1/evaluate endpoint, got %s", r.URL.Path), http.StatusBadRequest)
			return
		}

		body, _ := io.ReadAll(r.Body)
		var vReq jevRequest
		_ = json.Unmarshal(body, &vReq)
		if vReq.Model != "typesafe-ai/jev" {
			http.Error(w, fmt.Sprintf("expected model 'typesafe-ai/jev' in vercel mode, got '%s'", vReq.Model), http.StatusBadRequest)
			return
		}
		if q, ok := vReq.Questions["is_blocking"]; !ok || q.Type != "boolean" {
			http.Error(w, fmt.Sprintf("expected question type 'boolean' in vercel mode, got '%s'", q.Type), http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"is_blocking": {"noul": 0.92}}`)
	}))
	defer vercelServer.Close()

	vercelProvider := NewTypeSafeAIJevProvider("test-jev-key", vercelServer.URL+"/evaluate", JevModel)
	vResp, err := vercelProvider.Evaluate(context.Background(), DecisionRequest{
		State: "blocking?",
		Questions: map[string]DecisionQuestion{
			"is_blocking": {Type: QuestionTypeNoul, Instructions: "blocking?"},
		},
	})
	if err != nil {
		t.Fatalf("Vercel adaptive Jev Evaluate failed: %v", err)
	}
	if vProb, ok := vResp.GetNoul("is_blocking"); !ok || vProb != 0.92 {
		t.Fatalf("unexpected vercel noul prob: %f", vProb)
	}
}

func TestChoiceAnswer_UnmarshalJSON(t *testing.T) {
	cases := []struct {
		name     string
		jsonStr  string
		expected string
	}{
		{"documented choice field", `{"choice":"cancel","confidence":0.95}`, "cancel"},
		{"legacy selected field", `{"selected":"status_query","confidence":0.90}`, "status_query"},
		{"alternative value field", `{"value":"steer","confidence":0.85}`, "steer"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ca ChoiceAnswer
			if err := json.Unmarshal([]byte(tc.jsonStr), &ca); err != nil {
				t.Fatalf("failed to unmarshal: %v", err)
			}
			if ca.Value() != tc.expected {
				t.Fatalf("expected Value() = %s, got %s", tc.expected, ca.Value())
			}
			if ca.Choice != tc.expected {
				t.Fatalf("expected Choice = %s, got %s", tc.expected, ca.Choice)
			}
			if ca.Selected != tc.expected {
				t.Fatalf("expected Selected = %s, got %s", tc.expected, ca.Selected)
			}
		})
	}
}

func TestOpenAIDecisionProvider_Evaluate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-openai-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/v1/decisions") {
			http.Error(w, fmt.Sprintf("expected /v1/decisions endpoint, got %s", r.URL.Path), http.StatusBadRequest)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read error", http.StatusBadRequest)
			return
		}

		var req openAIDecisionRequest
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "json error", http.StatusBadRequest)
			return
		}

		if req.Model != OpenAIDecisionModel {
			http.Error(w, fmt.Sprintf("expected model %s, got %s", OpenAIDecisionModel, req.Model), http.StatusBadRequest)
			return
		}

		if req.Input != "User wants to stop right away" {
			http.Error(w, "unexpected input", http.StatusBadRequest)
			return
		}

		// Verify choices and levels are sent as object arrays
		for _, q := range req.Questions {
			if q.Type == "choice" {
				if len(q.Choices) == 0 || q.Choices[0].Value == "" {
					http.Error(w, "expected choices to be array of objects with 'value'", http.StatusBadRequest)
					return
				}
			}
			if q.Type == "score" {
				if len(q.Levels) == 0 || q.Levels[0].Label == "" {
					http.Error(w, "expected levels to be array of objects with 'label'", http.StatusBadRequest)
					return
				}
			}
		}

		w.Header().Set("Content-Type", "application/json")
		// Documented OpenAI Decisions API response schema: probabilities is an array of objects
		io.WriteString(w, `{
			"id": "dec_6luna_test123",
			"object": "decisions.response",
			"model": "gpt-6-luna",
			"answers": [
				{
					"name": "intent",
					"type": "choice",
					"choice": "cancel",
					"confidence": 0.97,
					"probabilities": [
						{ "value": "cancel", "probability": 0.97 },
						{ "value": "status_query", "probability": 0.02 },
						{ "value": "steer", "probability": 0.01 }
					]
				},
				{
					"name": "is_blocking",
					"type": "predicate",
					"probability": 0.91
				},
				{
					"name": "severity",
					"type": "score",
					"score": 3.5,
					"confidence": 0.88,
					"probabilities": [
						{ "value": 1, "probability": 0.02 },
						{ "value": 2, "probability": 0.10 },
						{ "value": 3, "probability": 0.88 }
					]
				}
			],
			"usage": {
				"input_tokens": 85,
				"output_tokens": 0,
				"total_tokens": 85
			}
		}`)
	}))
	defer server.Close()

	provider := NewOpenAIDecisionProvider("test-openai-key", server.URL, OpenAIDecisionModel)

	req := DecisionRequest{
		State: "User wants to stop right away",
		Questions: map[string]DecisionQuestion{
			"intent": {
				Type:         QuestionTypeChoice,
				Instructions: "Classify intent",
				Criteria:     []string{"status_query", "cancel", "steer"},
			},
			"is_blocking": {
				Type:         QuestionTypeNoul,
				Instructions: "Is this blocking?",
			},
			"severity": {
				Type:         QuestionTypeScore,
				Instructions: "Rate severity",
				Criteria:     []string{"1", "2", "3"},
			},
		},
	}

	resp, err := provider.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatalf("OpenAI Evaluate failed: %v", err)
	}

	if resp.Model != OpenAIDecisionModel {
		t.Fatalf("expected model %s, got %s", OpenAIDecisionModel, resp.Model)
	}

	if resp.Usage == nil || resp.Usage.PromptTokens != 85 || resp.Usage.CompletionTokens != 0 {
		t.Fatalf("unexpected usage: %+v", resp.Usage)
	}

	ca, ok := resp.GetChoice("intent")
	if !ok || ca.Value() != "cancel" || ca.Confidence != 0.97 {
		t.Fatalf("unexpected choice answer: %+v", ca)
	}

	prob, ok := resp.GetNoul("is_blocking")
	if !ok || prob != 0.91 {
		t.Fatalf("unexpected noul prob: %f", prob)
	}

	score, ok := resp.GetScore("severity")
	if !ok || score != 3.5 {
		t.Fatalf("unexpected score: %f", score)
	}

	sa, ok := resp.Answers["severity"].(ScoreAnswer)
	if !ok || sa.Probabilities == nil || sa.Probabilities["1"] != 0.02 || sa.Probabilities["2"] != 0.10 || sa.Probabilities["3"] != 0.88 {
		t.Fatalf("unexpected score probabilities with numeric values: %+v", sa.Probabilities)
	}
}

func TestOpenAIDecisionProvider_BaseURLNormalization(t *testing.T) {
	var requestedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id": "dec_1", "answers": [{"question_id": "urgent", "type": "noul", "probability": 0.5}], "usage": {"input_tokens": 10, "output_tokens": 0}}`)
	}))
	defer server.Close()

	// BaseURL has redundant /v1/decisions suffix
	provider := NewOpenAIDecisionProvider("key", server.URL+"/v1/decisions", OpenAIDecisionModel)

	req := DecisionRequest{
		State: "test state",
		Questions: map[string]DecisionQuestion{
			"urgent": {Type: QuestionTypeNoul, Instructions: "is urgent?"},
		},
	}
	if _, err := provider.Evaluate(context.Background(), req); err != nil {
		t.Fatalf("Evaluate failed: %v", err)
	}

	expectedPath := "/v1/decisions"
	if requestedPath != expectedPath {
		t.Fatalf("requestedPath = %q, want %q (no duplicate /v1/decisions)", requestedPath, expectedPath)
	}
}

func TestRegistry_DecisionProviders(t *testing.T) {
	reg := NewRegistry(nil)

	// Register OpenAIDecisionProvider for master
	openaiDec := NewOpenAIDecisionProvider("key", "", OpenAIDecisionModel)
	reg.RegisterDecisionProvider(openaiDec)

	p, err := reg.GetDecisionProvider(context.Background(), "openai-decision")
	if err != nil || p == nil {
		t.Fatalf("expected to get openai-decision: %v", err)
	}

	// Register Clef
	clef := NewCloudflareClefProvider("acc", "token", ClefFlashModel)
	reg.RegisterDecisionProvider(clef)

	p, err = reg.GetDecisionProvider(context.Background(), "cloudflare-clef")
	if err != nil || p == nil {
		t.Fatalf("expected to get cloudflare-clef: %v", err)
	}

	// Register Jev
	jev := NewTypeSafeAIJevProvider("key", "", JevModel)
	reg.RegisterDecisionProvider(jev)

	p, err = reg.GetDecisionProvider(context.Background(), "typesafe-jev")
	if err != nil || p == nil {
		t.Fatalf("expected to get typesafe-jev: %v", err)
	}

	// Unregister Jev
	reg.UnregisterDecisionProviderForTenant(MasterTenantID, "typesafe-jev")
	if _, err := reg.GetDecisionProvider(context.Background(), "typesafe-jev"); err == nil {
		t.Fatal("expected typesafe-jev to be unregistered")
	}
}

func TestJev_ScoreCriteriaRubricOrdering(t *testing.T) {
	// Test map with numeric string keys preserves sorted rubric order: 0, 1, 2
	mapInput := map[string]any{
		"2": "High risk",
		"0": "Low risk",
		"1": "Medium risk",
	}
	criteria := normalizeJevScoreCriteria(mapInput)
	if len(criteria) != 3 {
		t.Fatalf("expected 3 criteria, got %d", len(criteria))
	}
	if criteria[0] != "Low risk" || criteria[1] != "Medium risk" || criteria[2] != "High risk" {
		t.Fatalf("expected sorted numeric rubric order, got %v", criteria)
	}

	// Test map[string]string
	strMap := map[string]string{
		"step3": "final stage",
		"step1": "initial stage",
		"step2": "middle stage",
	}
	criteriaStr := normalizeJevScoreCriteria(strMap)
	if len(criteriaStr) != 3 {
		t.Fatalf("expected 3 criteria, got %d", len(criteriaStr))
	}
	if criteriaStr[0] != "initial stage" || criteriaStr[1] != "middle stage" || criteriaStr[2] != "final stage" {
		t.Fatalf("expected sorted key rubric order, got %v", criteriaStr)
	}
}

func TestRegistry_DisabledTenantDecisionProvider_DoesNotFallBackToMaster(t *testing.T) {
	tenantID := uuid.New()
	reg := NewRegistry(func(ctx context.Context) uuid.UUID { return tenantID })

	// Register master provider named "openai-decision"
	masterScorer := NewOpenAIDecisionProvider("master-key", "", OpenAIDecisionModel)
	reg.RegisterDecisionProvider(masterScorer)

	// Register tenant provider also named "openai-decision"
	tenantScorer := NewOpenAIDecisionProvider("tenant-key", "", OpenAIDecisionModel)
	reg.RegisterDecisionProviderForTenant(tenantID, tenantScorer)

	// While enabled, tenant gets its own provider
	p, err := reg.GetDecisionProviderForTenant(tenantID, "openai-decision")
	if err != nil || p == nil {
		t.Fatalf("expected tenant provider: %v", err)
	}

	// Disable tenant provider
	reg.RegisterDisabledDecisionProviderForTenant(tenantID, "openai-decision")

	// Tenant lookup must return error indicating disabled, and NOT fall back to master
	p, err = reg.GetDecisionProviderForTenant(tenantID, "openai-decision")
	if err == nil {
		t.Fatalf("expected error for disabled tenant provider, got provider %v", p)
	}
	if !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("expected error mentioning disabled, got: %v", err)
	}

	// Explicit master provider lookup (tenant without its own provider) still succeeds
	masterOnly := NewCloudflareClefProvider("acc", "token", ClefFlashModel)
	reg.RegisterDecisionProvider(masterOnly)

	pMaster, err := reg.GetDecisionProviderForTenant(tenantID, "cloudflare-clef")
	if err != nil || pMaster == nil {
		t.Fatalf("expected fallback to master for provider tenant doesn't own: %v", err)
	}
}
