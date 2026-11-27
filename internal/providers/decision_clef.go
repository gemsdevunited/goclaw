package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// ClefFlashModel is the 9B lightweight low-latency model (sub-40ms).
	ClefFlashModel = "@cf/cloudflare/clef-flash"
	// ClefModel is the 27B multimodal full decision model.
	ClefModel = "@cf/cloudflare/clef"

	cloudflareAIBaseURL = "https://api.cloudflare.com/client/v4/accounts"
)

// CloudflareClefProvider implements DecisionProvider for Cloudflare Workers AI Clef models.
type CloudflareClefProvider struct {
	name      string
	accountID string
	apiToken  string
	model     string
	baseURL   string
	client    *http.Client
}

// NewCloudflareClefProvider creates a new decision provider for Cloudflare Clef.
func NewCloudflareClefProvider(accountID, apiToken, defaultModel string) *CloudflareClefProvider {
	if defaultModel == "" {
		defaultModel = ClefFlashModel
	}
	return &CloudflareClefProvider{
		accountID: accountID,
		apiToken:  apiToken,
		model:     defaultModel,
		baseURL:   cloudflareAIBaseURL,
		client:    NewDefaultHTTPClient(),
	}
}

// WithName sets the provider identifier or alias (e.g. from DB).
func (p *CloudflareClefProvider) WithName(name string) *CloudflareClefProvider {
	if name != "" {
		p.name = name
	}
	return p
}

// WithBaseURL overrides the base API endpoint (useful for testing or private proxies).
func (p *CloudflareClefProvider) WithBaseURL(url string) *CloudflareClefProvider {
	url = strings.TrimRight(url, "/")
	url = strings.TrimSuffix(url, "/ai/run")
	url = strings.TrimSuffix(url, "/ai")
	url = strings.TrimRight(url, "/")
	if p.accountID != "" && strings.HasSuffix(url, "/"+p.accountID) {
		url = strings.TrimSuffix(url, "/"+p.accountID)
	}
	p.baseURL = strings.TrimRight(url, "/")
	return p
}

// WithClient sets a custom HTTP client.
func (p *CloudflareClefProvider) WithClient(client *http.Client) *CloudflareClefProvider {
	p.client = client
	return p
}

func (p *CloudflareClefProvider) Name() string {
	if p.name != "" {
		return p.name
	}
	return "cloudflare-clef"
}

func (p *CloudflareClefProvider) CanonicalName() string {
	return "cloudflare-clef"
}

func (p *CloudflareClefProvider) DefaultModel() string {
	return p.model
}

// cfClefRequest matches Cloudflare Workers AI API run payload.
type cfClefRequest struct {
	Model     string                      `json:"model"`
	State     string                      `json:"state"`
	Images    []string                    `json:"images,omitempty"`
	Questions map[string]DecisionQuestion `json:"questions"`
}

// cfClefEnvelope captures Cloudflare's standard REST API response format.
type cfClefEnvelope struct {
	Success bool `json:"success"`
	Result  struct {
		Answers map[string]json.RawMessage `json:"answers"`
		Usage   *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage,omitempty"`
	} `json:"result"`
	// Also capture top-level answers if proxied without envelope
	Answers map[string]json.RawMessage `json:"answers,omitempty"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors,omitempty"`
}

// normalizeClefURLModel ensures the full Cloudflare model ID is used in the REST URL path
// (e.g. "clef-flash" -> "@cf/cloudflare/clef-flash").
func normalizeClefURLModel(model string) string {
	if strings.HasPrefix(model, "@cf/") {
		return model
	}
	return "@cf/cloudflare/" + model
}

// normalizeClefBodyModel extracts the short model selector required in Cloudflare's request body
// (e.g. "@cf/cloudflare/clef-flash" -> "clef-flash", "@cf/cloudflare/clef" -> "clef").
func normalizeClefBodyModel(fullModel string) string {
	clean := strings.TrimPrefix(fullModel, "@cf/cloudflare/")
	clean = strings.TrimPrefix(clean, "@cf/")
	return clean
}

func (p *CloudflareClefProvider) Evaluate(ctx context.Context, req DecisionRequest) (*DecisionResponse, error) {
	if err := ValidateDecisionRequest(req); err != nil {
		return nil, fmt.Errorf("clef: invalid request: %w", err)
	}
	if p.accountID == "" || p.apiToken == "" {
		return nil, fmt.Errorf("clef: accountID and apiToken must be configured")
	}

	modelID := req.Model
	if modelID == "" {
		modelID = p.model
	}

	urlModel := normalizeClefURLModel(modelID)
	bodyModel := normalizeClefBodyModel(modelID)

	payload := cfClefRequest{
		Model:     bodyModel,
		State:     req.State,
		Images:    req.Images,
		Questions: req.Questions,
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("clef: marshal request: %w", err)
	}

	baseURL := strings.TrimRight(p.baseURL, "/")
	baseURL = strings.TrimSuffix(baseURL, "/ai/run")
	baseURL = strings.TrimSuffix(baseURL, "/ai")
	baseURL = strings.TrimRight(baseURL, "/")
	if p.accountID != "" && strings.HasSuffix(baseURL, "/"+p.accountID) {
		baseURL = strings.TrimSuffix(baseURL, "/"+p.accountID)
	}
	baseURL = strings.TrimRight(baseURL, "/")

	url := fmt.Sprintf("%s/%s/ai/run/%s", baseURL, p.accountID, urlModel)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("clef: create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiToken)

	start := time.Now()
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("clef: http request: %w", err)
	}
	defer resp.Body.Close()

	durationMs := time.Since(start).Milliseconds()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("clef: read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("clef: http error %d: %s", resp.StatusCode, string(respBytes))
	}

	var env cfClefEnvelope
	if err := json.Unmarshal(respBytes, &env); err != nil {
		return nil, fmt.Errorf("clef: parse json response: %w", err)
	}

	rawAnswers := env.Result.Answers
	if len(rawAnswers) == 0 && len(env.Answers) > 0 {
		rawAnswers = env.Answers
	}

	parsedAnswers := make(map[string]any, len(rawAnswers))
	for qKey, rawVal := range rawAnswers {
		qDef, exists := req.Questions[qKey]
		if !exists {
			parsedAnswers[qKey] = rawVal
			continue
		}

		switch qDef.Type {
		case QuestionTypeChoice:
			var ca ChoiceAnswer
			if err := json.Unmarshal(rawVal, &ca); err == nil {
				if ca.Choice == "" && ca.Selected != "" {
					ca.Choice = ca.Selected
				} else if ca.Selected == "" && ca.Choice != "" {
					ca.Selected = ca.Choice
				}
				parsedAnswers[qKey] = ca
			} else {
				parsedAnswers[qKey] = rawVal
			}
		case QuestionTypeScore:
			var sa ScoreAnswer
			if err := json.Unmarshal(rawVal, &sa); err == nil {
				parsedAnswers[qKey] = sa
			} else {
				parsedAnswers[qKey] = rawVal
			}
		case QuestionTypeNoul:
			var rawItem struct {
				Noul        *float64 `json:"noul"`
				Probability *float64 `json:"probability"`
				Value       any      `json:"value"`
			}
			if err := json.Unmarshal(rawVal, &rawItem); err == nil {
				prob := 0.0
				hasVal := false
				if rawItem.Noul != nil {
					prob = *rawItem.Noul
					hasVal = true
				} else if rawItem.Probability != nil {
					prob = *rawItem.Probability
					hasVal = true
				} else if rawItem.Value != nil {
					switch v := rawItem.Value.(type) {
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
			}
		default:
			parsedAnswers[qKey] = rawVal
		}
	}

	var usage *Usage
	if env.Result.Usage != nil {
		usage = &Usage{
			PromptTokens:     env.Result.Usage.PromptTokens,
			CompletionTokens: env.Result.Usage.CompletionTokens,
			TotalTokens:      env.Result.Usage.PromptTokens + env.Result.Usage.CompletionTokens,
		}
	}

	return &DecisionResponse{
		Answers: parsedAnswers,
		Usage:   usage,
		Latency: durationMs,
		Model:   modelID,
	}, nil
}
