package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

const (
	// OpenAIDecisionModel is the high-performance decision model specialized for classification and routing.
	OpenAIDecisionModel  = "gpt-6-luna"
	defaultOpenAIBaseURL = "https://api.openai.com/v1"
)

// OpenAIDecisionProvider implements DecisionProvider for OpenAI's Decisions API (POST /v1/decisions).
type OpenAIDecisionProvider struct {
	name    string
	apiKey  string
	baseURL string
	model   string
	client  *http.Client
}

// NewOpenAIDecisionProvider creates a provider targeting OpenAI's Decisions API.
func NewOpenAIDecisionProvider(apiKey, baseURL, defaultModel string) *OpenAIDecisionProvider {
	if baseURL == "" {
		baseURL = defaultOpenAIBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")
	baseURL = strings.TrimSuffix(baseURL, "/decisions")
	baseURL = strings.TrimRight(baseURL, "/")
	if defaultModel == "" {
		defaultModel = OpenAIDecisionModel
	}
	return &OpenAIDecisionProvider{
		apiKey:  apiKey,
		baseURL: baseURL,
		model:   defaultModel,
		client:  NewDefaultHTTPClient(),
	}
}

// WithBaseURL overrides the base API endpoint (useful for testing or private proxies).
func (p *OpenAIDecisionProvider) WithBaseURL(url string) *OpenAIDecisionProvider {
	url = strings.TrimRight(url, "/")
	url = strings.TrimSuffix(url, "/decisions")
	p.baseURL = strings.TrimRight(url, "/")
	return p
}

// WithName sets the provider identifier or alias (e.g. from DB).
func (p *OpenAIDecisionProvider) WithName(name string) *OpenAIDecisionProvider {
	if name != "" {
		p.name = name
	}
	return p
}

// WithClient sets a custom HTTP client.
func (p *OpenAIDecisionProvider) WithClient(client *http.Client) *OpenAIDecisionProvider {
	p.client = client
	return p
}

func (p *OpenAIDecisionProvider) Name() string {
	if p.name != "" {
		return p.name
	}
	return "openai-decision"
}

func (p *OpenAIDecisionProvider) CanonicalName() string {
	return "openai-decision"
}

func (p *OpenAIDecisionProvider) DefaultModel() string {
	return p.model
}

type openAIDecisionChoiceOption struct {
	Value       string `json:"value"`
	Description string `json:"description,omitempty"`
}

type openAIDecisionScoreLevel struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type openAIDecisionQuestion struct {
	Name         string                       `json:"name"`
	Instructions string                       `json:"instructions"`
	Type         string                       `json:"type"` // "predicate", "choice", "score"
	Choices      []openAIDecisionChoiceOption `json:"choices,omitempty"`
	Levels       []openAIDecisionScoreLevel   `json:"levels,omitempty"`
}

type openAIDecisionRequest struct {
	Model     string                   `json:"model"`
	Input     string                   `json:"input"`
	Questions []openAIDecisionQuestion `json:"questions"`
}

type openAIProbabilityItem struct {
	Value       any     `json:"value,omitempty"`
	Label       string  `json:"label,omitempty"`
	Choice      string  `json:"choice,omitempty"`
	Probability float64 `json:"probability"`
}

type openAIDecisionAnswerItem struct {
	Name          string          `json:"name"`
	Type          string          `json:"type"`
	Probability   *float64        `json:"probability,omitempty"`
	Choice        *string         `json:"choice,omitempty"`
	Selected      *string         `json:"selected,omitempty"`
	Confidence    *float64        `json:"confidence,omitempty"`
	Score         *float64        `json:"score,omitempty"`
	Probabilities json.RawMessage `json:"probabilities,omitempty"`
	Refusal       *string         `json:"refusal,omitempty"`
}

type openAIDecisionResponse struct {
	ID      string                     `json:"id"`
	Object  string                     `json:"object"`
	Model   string                     `json:"model"`
	Answers []openAIDecisionAnswerItem `json:"answers"`
	Usage   *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage,omitempty"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

// parseOpenAIProbabilities parses the probabilities returned by OpenAI Decisions API,
// which is officially an array of objects `[{"value": ..., "probability": ...}]`
// (where value can be string or numeric score level) or `[{"label": "...", "probability": ...}]`,
// while remaining compatible with a direct map envelope.
func parseOpenAIProbabilities(raw json.RawMessage) map[string]float64 {
	if len(raw) == 0 {
		return nil
	}

	// 1. Array of objects: [{"value": "...", "probability": 0.95}] or [{"value": 1, "probability": 0.1}]
	var items []openAIProbabilityItem
	if err := json.Unmarshal(raw, &items); err == nil && len(items) > 0 {
		res := make(map[string]float64, len(items))
		for _, it := range items {
			key := ""
			switch v := it.Value.(type) {
			case string:
				key = v
			case float64:
				if v == float64(int64(v)) {
					key = fmt.Sprintf("%d", int64(v))
				} else {
					key = fmt.Sprintf("%v", v)
				}
			case json.Number:
				key = v.String()
			}
			if key == "" {
				key = it.Label
			}
			if key == "" {
				key = it.Choice
			}
			if key != "" {
				res[key] = it.Probability
			}
		}
		return res
	}

	// 2. Direct map fallback: {"cancel": 0.95}
	var directMap map[string]float64
	if err := json.Unmarshal(raw, &directMap); err == nil {
		return directMap
	}

	return nil
}

// Evaluate evaluates a DecisionRequest against OpenAI's Decisions API (POST /v1/decisions).
func (p *OpenAIDecisionProvider) Evaluate(ctx context.Context, req DecisionRequest) (*DecisionResponse, error) {
	if err := ValidateDecisionRequest(req); err != nil {
		return nil, fmt.Errorf("openai-decision: invalid request: %w", err)
	}
	if p.apiKey == "" {
		return nil, fmt.Errorf("openai-decision: api key must be configured")
	}

	modelID := req.Model
	if modelID == "" {
		modelID = p.model
	}

	// Question order in array is preserved deterministically
	qKeys := make([]string, 0, len(req.Questions))
	for k := range req.Questions {
		qKeys = append(qKeys, k)
	}
	sort.Strings(qKeys)

	apiQuestions := make([]openAIDecisionQuestion, 0, len(qKeys))
	for _, k := range qKeys {
		qDef := req.Questions[k]
		q := openAIDecisionQuestion{
			Name:         k,
			Instructions: qDef.Instructions,
		}

		switch qDef.Type {
		case QuestionTypeChoice:
			q.Type = "choice"
			switch c := qDef.Criteria.(type) {
			case []string:
				choices := make([]openAIDecisionChoiceOption, 0, len(c))
				for _, opt := range c {
					choices = append(choices, openAIDecisionChoiceOption{Value: opt})
				}
				q.Choices = choices
			case map[string]string:
				keys := make([]string, 0, len(c))
				for opt := range c {
					keys = append(keys, opt)
				}
				sort.Strings(keys)
				choices := make([]openAIDecisionChoiceOption, 0, len(keys))
				for _, opt := range keys {
					choices = append(choices, openAIDecisionChoiceOption{
						Value:       opt,
						Description: c[opt],
					})
				}
				q.Choices = choices
			}
		case QuestionTypeScore:
			q.Type = "score"
			switch c := qDef.Criteria.(type) {
			case []string:
				levels := make([]openAIDecisionScoreLevel, 0, len(c))
				for _, opt := range c {
					levels = append(levels, openAIDecisionScoreLevel{Label: opt})
				}
				q.Levels = levels
			case map[string]string:
				keys := make([]string, 0, len(c))
				for opt := range c {
					keys = append(keys, opt)
				}
				sort.Strings(keys)
				levels := make([]openAIDecisionScoreLevel, 0, len(keys))
				for _, opt := range keys {
					levels = append(levels, openAIDecisionScoreLevel{
						Label:       opt,
						Description: c[opt],
					})
				}
				q.Levels = levels
			default:
				q.Levels = []openAIDecisionScoreLevel{
					{Label: "low"},
					{Label: "medium"},
					{Label: "high"},
					{Label: "critical"},
				}
			}
		case QuestionTypeNoul:
			q.Type = "predicate"
		default:
			q.Type = "choice"
		}
		apiQuestions = append(apiQuestions, q)
	}

	payload := openAIDecisionRequest{
		Model:     modelID,
		Input:     req.State,
		Questions: apiQuestions,
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("openai-decision: marshal request: %w", err)
	}

	endpoint := strings.TrimRight(p.baseURL, "/")
	endpoint = strings.TrimSuffix(endpoint, "/decisions")
	endpoint = strings.TrimRight(endpoint, "/")
	if !strings.HasSuffix(endpoint, "/v1") {
		endpoint += "/v1"
	}
	url := endpoint + "/decisions"

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("openai-decision: create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	start := time.Now()
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai-decision: http request: %w", err)
	}
	defer resp.Body.Close()

	durationMs := time.Since(start).Milliseconds()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("openai-decision: read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("openai-decision: http error %d: %s", resp.StatusCode, string(respBytes))
	}

	var decResp openAIDecisionResponse
	if err := json.Unmarshal(respBytes, &decResp); err != nil {
		return nil, fmt.Errorf("openai-decision: parse response: %w", err)
	}

	if decResp.Error != nil && decResp.Error.Message != "" {
		return nil, fmt.Errorf("openai-decision: api error: %s", decResp.Error.Message)
	}

	parsedAnswers := make(map[string]any, len(decResp.Answers))
	for _, ans := range decResp.Answers {
		qDef, exists := req.Questions[ans.Name]
		if !exists {
			continue
		}

		probs := parseOpenAIProbabilities(ans.Probabilities)

		switch qDef.Type {
		case QuestionTypeChoice:
			choiceVal := ""
			if ans.Choice != nil && *ans.Choice != "" {
				choiceVal = *ans.Choice
			} else if ans.Selected != nil && *ans.Selected != "" {
				choiceVal = *ans.Selected
			}
			conf := resolveDecisionConfidence(ans.Confidence, choiceVal, probs)
			parsedAnswers[ans.Name] = ChoiceAnswer{
				Choice:        choiceVal,
				Selected:      choiceVal,
				Confidence:    conf,
				Probabilities: probs,
			}
		case QuestionTypeScore:
			scoreVal := 0.0
			if ans.Score != nil {
				scoreVal = *ans.Score
			}
			parsedAnswers[ans.Name] = ScoreAnswer{
				Score:         scoreVal,
				Confidence:    resolveDecisionConfidence(ans.Confidence, "", nil),
				Probabilities: probs,
			}
		case QuestionTypeNoul:
			if ans.Probability != nil {
				probVal := *ans.Probability
				parsedAnswers[ans.Name] = NoulAnswer{
					Probability: probVal,
					Noul:        probVal,
				}
			}
		default:
			choiceVal := ""
			if ans.Choice != nil {
				choiceVal = *ans.Choice
			}
			parsedAnswers[ans.Name] = ChoiceAnswer{
				Choice:        choiceVal,
				Selected:      choiceVal,
				Confidence:    resolveDecisionConfidence(ans.Confidence, choiceVal, probs),
				Probabilities: probs,
			}
		}
	}

	var usage *Usage
	if decResp.Usage != nil {
		usage = &Usage{
			PromptTokens:     decResp.Usage.InputTokens,
			CompletionTokens: decResp.Usage.OutputTokens,
			TotalTokens:      decResp.Usage.TotalTokens,
		}
	}

	return &DecisionResponse{
		Answers: parsedAnswers,
		Usage:   usage,
		Latency: durationMs,
		Model:   modelID,
	}, nil
}
