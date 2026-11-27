package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// JevModel is the default TypeSafe AI flagship decision model alias.
	JevModel = "jev-latest"

	defaultJevBaseURL = "https://api.typesafe.ai"
)

// resolveJevEndpoint determines the target endpoint URL and whether to use Vercel AI Gateway conventions.
func resolveJevEndpoint(baseURL string) (endpointURL string, isVercel bool) {
	if baseURL == "" {
		baseURL = defaultJevBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")

	// If configured for Vercel AI Gateway (e.g. ai-gateway.vercel.sh or api.vercel.com)
	if strings.Contains(baseURL, "vercel") || strings.HasSuffix(baseURL, "/evaluate") {
		if strings.HasSuffix(baseURL, "/v1/evaluate") {
			return baseURL, true
		}
		baseURL = strings.TrimSuffix(baseURL, "/evaluate")
		if !strings.HasSuffix(baseURL, "/v1") {
			baseURL += "/v1"
		}
		return baseURL + "/evaluate", true
	}

	// TypeSafe AI native endpoint: POST /v1/systemone
	if strings.HasSuffix(baseURL, "/v1/systemone") {
		return baseURL, false
	}
	baseURL = strings.TrimSuffix(baseURL, "/systemone")
	if !strings.HasSuffix(baseURL, "/v1") {
		baseURL += "/v1"
	}
	return baseURL + "/systemone", false
}

// TypeSafeAIJevProvider implements DecisionProvider for TypeSafe AI Jev.
type TypeSafeAIJevProvider struct {
	name   string
	apiKey string
	apiURL string
	model  string
	client *http.Client
}

// NewTypeSafeAIJevProvider creates a new decision provider for TypeSafe AI Jev.
func NewTypeSafeAIJevProvider(apiKey, apiURL, defaultModel string) *TypeSafeAIJevProvider {
	if apiURL == "" {
		apiURL = defaultJevBaseURL
	}
	if defaultModel == "" {
		defaultModel = JevModel
	}
	return &TypeSafeAIJevProvider{
		apiKey: apiKey,
		apiURL: apiURL,
		model:  defaultModel,
		client: NewDefaultHTTPClient(),
	}
}

// WithName sets the provider identifier or alias (e.g. from DB).
func (p *TypeSafeAIJevProvider) WithName(name string) *TypeSafeAIJevProvider {
	if name != "" {
		p.name = name
	}
	return p
}

// WithClient sets a custom HTTP client.
func (p *TypeSafeAIJevProvider) WithClient(client *http.Client) *TypeSafeAIJevProvider {
	p.client = client
	return p
}

func (p *TypeSafeAIJevProvider) Name() string {
	if p.name != "" {
		return p.name
	}
	return "typesafe-jev"
}

func (p *TypeSafeAIJevProvider) CanonicalName() string {
	return "typesafe-jev"
}

func (p *TypeSafeAIJevProvider) DefaultModel() string {
	return p.model
}

// jevQuestionPayload matches Jev evaluate question schema.
type jevQuestionPayload struct {
	Type         string   `json:"type"`
	Instructions string   `json:"instructions"`
	Options      []string `json:"options,omitempty"`
	Min          float64  `json:"min,omitempty"`
	Max          float64  `json:"max,omitempty"`
	Criteria     any      `json:"criteria,omitempty"`
}

type jevRequest struct {
	Model     string                        `json:"model"`
	State     string                        `json:"state"`
	Questions map[string]jevQuestionPayload `json:"questions"`
}

type jevAnswerItem struct {
	Type          string             `json:"type,omitempty"`
	Choice        *string            `json:"choice,omitempty"`
	Selected      *string            `json:"selected,omitempty"`
	Value         any                `json:"value,omitempty"`
	Score         *float64           `json:"score,omitempty"`
	Noul          *float64           `json:"noul,omitempty"`
	Probability   *float64           `json:"probability,omitempty"`
	Confidence    *float64           `json:"confidence,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
}

func (p *TypeSafeAIJevProvider) Evaluate(ctx context.Context, req DecisionRequest) (*DecisionResponse, error) {
	if err := ValidateDecisionRequest(req); err != nil {
		return nil, fmt.Errorf("jev: invalid request: %w", err)
	}
	if p.apiKey == "" {
		return nil, fmt.Errorf("jev: apiKey must be configured")
	}

	modelID := req.Model
	if modelID == "" {
		modelID = p.model
	}

	endpointURL, isVercel := resolveJevEndpoint(p.apiURL)

	if isVercel {
		if modelID == "" || modelID == "jev-latest" {
			modelID = "typesafe-ai/jev"
		}
	} else {
		if modelID == "" || modelID == "typesafe-ai/jev" {
			modelID = "jev-latest"
		}
	}

	questionsPayload := make(map[string]jevQuestionPayload, len(req.Questions))
	for k, q := range req.Questions {
		typeStr := string(q.Type)
		var crit any = q.Criteria
		var options []string

		switch q.Type {
		case QuestionTypeNoul:
			if isVercel {
				typeStr = "boolean"
			} else {
				typeStr = "noul"
			}
			crit = nil // binary questions do not require criteria

		case QuestionTypeChoice:
			typeStr = "choice"
			// TypeSafe AI OpenAPI schema requires choice.criteria to be an object (map[string]string)
			switch c := q.Criteria.(type) {
			case map[string]string:
				crit = c
				for opt := range c {
					options = append(options, opt)
				}
			case map[string]any:
				crit = c
				for opt := range c {
					options = append(options, opt)
				}
			case []string:
				m := make(map[string]string, len(c))
				for _, opt := range c {
					m[opt] = opt
				}
				crit = m
				options = c
			default:
				crit = map[string]string{}
			}

		case QuestionTypeScore:
			typeStr = "score"
			// TypeSafe AI OpenAPI schema requires score.criteria to be an ordered array of level rubrics
			crit = normalizeJevScoreCriteria(q.Criteria)
		}

		jp := jevQuestionPayload{
			Type:         typeStr,
			Instructions: q.Instructions,
			Options:      options,
			Criteria:     crit,
		}
		questionsPayload[k] = jp
	}

	payload := jevRequest{
		Model:     modelID,
		State:     req.State,
		Questions: questionsPayload,
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("jev: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", endpointURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("jev: create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	start := time.Now()
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("jev: http request: %w", err)
	}
	defer resp.Body.Close()

	durationMs := time.Since(start).Milliseconds()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("jev: read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("jev: http error %d: %s", resp.StatusCode, string(respBytes))
	}

	// Jev response can be { "answers": { ... } } or directly { "question_name": { "value": ... } }
	var directMap map[string]json.RawMessage
	if err := json.Unmarshal(respBytes, &directMap); err != nil {
		return nil, fmt.Errorf("jev: parse json response: %w", err)
	}

	answersRaw := directMap
	if subRaw, ok := directMap["answers"]; ok {
		var subMap map[string]json.RawMessage
		if err := json.Unmarshal(subRaw, &subMap); err == nil {
			answersRaw = subMap
		}
	}

	parsedAnswers := make(map[string]any, len(answersRaw))
	for qKey, rawVal := range answersRaw {
		if qKey == "usage" || qKey == "model" {
			continue
		}
		qDef, exists := req.Questions[qKey]
		if !exists {
			parsedAnswers[qKey] = rawVal
			continue
		}

		var item jevAnswerItem
		if err := json.Unmarshal(rawVal, &item); err != nil {
			parsedAnswers[qKey] = rawVal
			continue
		}

		switch qDef.Type {
		case QuestionTypeChoice:
			selected := ""
			if item.Choice != nil && *item.Choice != "" {
				selected = *item.Choice
			} else if item.Selected != nil && *item.Selected != "" {
				selected = *item.Selected
			} else if item.Value != nil {
				selected = fmt.Sprintf("%v", item.Value)
			}
			parsedAnswers[qKey] = ChoiceAnswer{
				Choice:        selected,
				Selected:      selected,
				Confidence:    resolveDecisionConfidence(item.Confidence, selected, item.Probabilities),
				Probabilities: item.Probabilities,
			}
		case QuestionTypeScore:
			score := 0.0
			if item.Score != nil {
				score = *item.Score
			} else if item.Value != nil {
				switch v := item.Value.(type) {
				case float64:
					score = v
				case int:
					score = float64(v)
				}
			}
			parsedAnswers[qKey] = ScoreAnswer{
				Score:         score,
				Confidence:    resolveDecisionConfidence(item.Confidence, "", nil),
				Probabilities: item.Probabilities,
			}
		case QuestionTypeNoul:
			prob := 0.0
			hasVal := false
			if item.Noul != nil {
				prob = *item.Noul
				hasVal = true
			} else if item.Probability != nil {
				prob = *item.Probability
				hasVal = true
			} else if item.Value != nil {
				switch v := item.Value.(type) {
				case float64:
					prob = v
					hasVal = true
				case int:
					prob = float64(v)
					hasVal = true
				case bool:
					if v {
						prob = 1.0
					}
					hasVal = true
				}
			}
			if hasVal {
				parsedAnswers[qKey] = NoulAnswer{
					Probability: prob,
					Noul:        prob,
				}
			}
		default:
			parsedAnswers[qKey] = item.Value
		}
	}

	servedModel := modelID
	if mRaw, ok := directMap["model"]; ok {
		var sm string
		if err := json.Unmarshal(mRaw, &sm); err == nil && sm != "" {
			servedModel = sm
		}
	}

	var usage *Usage
	if uRaw, ok := directMap["usage"]; ok {
		var ju struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
			InputTokens      int `json:"input_tokens"`
			OutputTokens     int `json:"output_tokens"`
		}
		if err := json.Unmarshal(uRaw, &ju); err == nil {
			pt := ju.PromptTokens
			if pt == 0 {
				pt = ju.InputTokens
			}
			ct := ju.CompletionTokens
			if ct == 0 {
				ct = ju.OutputTokens
			}
			tt := ju.TotalTokens
			if tt == 0 {
				tt = pt + ct
			}
			usage = &Usage{
				PromptTokens:     pt,
				CompletionTokens: ct,
				TotalTokens:      tt,
			}
		}
	}

	return &DecisionResponse{
		Answers: parsedAnswers,
		Usage:   usage,
		Latency: durationMs,
		Model:   servedModel,
	}, nil
}

// normalizeJevScoreCriteria converts any caller-provided criteria format into an ordered array of score levels.
func normalizeJevScoreCriteria(c any) []string {
	if c == nil {
		return []string{"low", "medium", "high"}
	}
	switch v := c.(type) {
	case []string:
		if len(v) > 0 {
			return v
		}
	case []any:
		arr := make([]string, 0, len(v))
		for _, item := range v {
			arr = append(arr, fmt.Sprintf("%v", item))
		}
		if len(arr) > 0 {
			return arr
		}
	case map[string]string:
		if len(v) > 0 {
			keys := make([]string, 0, len(v))
			for k := range v {
				keys = append(keys, k)
			}
			sortRubricKeys(keys)
			arr := make([]string, 0, len(keys))
			for _, k := range keys {
				arr = append(arr, v[k])
			}
			return arr
		}
	case map[string]any:
		if len(v) > 0 {
			keys := make([]string, 0, len(v))
			for k := range v {
				keys = append(keys, k)
			}
			sortRubricKeys(keys)
			arr := make([]string, 0, len(keys))
			for _, k := range keys {
				arr = append(arr, fmt.Sprintf("%v", v[k]))
			}
			return arr
		}
	}
	return []string{"low", "medium", "high"}
}

func sortRubricKeys(keys []string) {
	allNumeric := true
	for _, k := range keys {
		if _, err := strconv.ParseFloat(k, 64); err != nil {
			allNumeric = false
			break
		}
	}
	if allNumeric {
		sort.Slice(keys, func(i, j int) bool {
			ni, _ := strconv.ParseFloat(keys[i], 64)
			nj, _ := strconv.ParseFloat(keys[j], 64)
			return ni < nj
		})
	} else {
		sort.Strings(keys)
	}
}
