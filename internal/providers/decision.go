package providers

import (
	"context"
	"encoding/json"
	"fmt"
)

// DecisionQuestionType represents the evaluation format for a decision question.
type DecisionQuestionType string

const (
	// QuestionTypeChoice evaluates discrete categorical selection.
	QuestionTypeChoice DecisionQuestionType = "choice"
	// QuestionTypeScore evaluates a scaled or tiered score (e.g. 0 to 1, or ordinal criteria).
	QuestionTypeScore DecisionQuestionType = "score"
	// QuestionTypeNoul evaluates a binary yes/no probability (P(yes) in [0, 1]).
	QuestionTypeNoul DecisionQuestionType = "noul"
)

// ValidDecisionQuestionTypes lookup table for fast validation.
var ValidDecisionQuestionTypes = map[DecisionQuestionType]bool{
	QuestionTypeChoice: true,
	QuestionTypeScore:  true,
	QuestionTypeNoul:   true,
}

// DecisionQuestion defines the schema for a single question evaluated against a state.
type DecisionQuestion struct {
	Type         DecisionQuestionType `json:"type"`
	Instructions string               `json:"instructions"`
	// Criteria defines the allowed options or scoring levels.
	// For Choice: map[string]string (key -> description) or []string.
	// For Score: []string (ordered level labels) or map[string]any.
	Criteria any `json:"criteria,omitempty"`
}

// DecisionRequest contains the state to inspect and the questions to evaluate.
type DecisionRequest struct {
	Model     string                      `json:"model,omitempty"`
	State     string                      `json:"state"`
	Images    []string                    `json:"images,omitempty"` // base64 or URL strings
	Questions map[string]DecisionQuestion `json:"questions"`
}

// ChoiceAnswer is returned for QuestionTypeChoice questions.
// Documented decision models (Cloudflare Clef, TypeSafe AI / Vercel Jev) use "choice".
// "selected" and "value" are supported for backward and alternate format compatibility.
type ChoiceAnswer struct {
	Choice        string             `json:"choice"`
	Selected      string             `json:"selected,omitempty"`
	Confidence    float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
}

// UnmarshalJSON decodes choice, selected, or value fields for compatibility with
// both documented decision model APIs and legacy/alternate envelopes.
func (c *ChoiceAnswer) UnmarshalJSON(data []byte) error {
	type Alias ChoiceAnswer
	var aux struct {
		Alias
		Choice   *string `json:"choice"`
		Selected *string `json:"selected"`
		Value    any     `json:"value"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	c.Confidence = aux.Confidence
	c.Probabilities = aux.Probabilities

	val := ""
	if aux.Choice != nil && *aux.Choice != "" {
		val = *aux.Choice
	} else if aux.Selected != nil && *aux.Selected != "" {
		val = *aux.Selected
	} else if aux.Value != nil {
		val = fmt.Sprintf("%v", aux.Value)
	}

	c.Choice = val
	c.Selected = val
	return nil
}

// Value returns the chosen option string.
func (c *ChoiceAnswer) Value() string {
	if c == nil {
		return ""
	}
	if c.Choice != "" {
		return c.Choice
	}
	return c.Selected
}

// ScoreAnswer is returned for QuestionTypeScore questions.
type ScoreAnswer struct {
	Score         float64            `json:"score"`
	Confidence    float64            `json:"confidence,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
}

// UnmarshalJSON decodes score answers supporting both direct "score", generic "value", or raw number.
func (s *ScoreAnswer) UnmarshalJSON(data []byte) error {
	var f float64
	if err := json.Unmarshal(data, &f); err == nil {
		s.Score = f
		return nil
	}

	var aux struct {
		Score         *float64           `json:"score"`
		Value         any                `json:"value"`
		Confidence    float64            `json:"confidence"`
		Probabilities map[string]float64 `json:"probabilities"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	s.Confidence = aux.Confidence
	s.Probabilities = aux.Probabilities
	if aux.Score != nil {
		s.Score = *aux.Score
	} else if aux.Value != nil {
		switch v := aux.Value.(type) {
		case float64:
			s.Score = v
		case int:
			s.Score = float64(v)
		}
	}
	return nil
}

// NoulAnswer is returned for QuestionTypeNoul questions.
// Supports both Cloudflare Workers AI "noul" key and generic "probability" key.
type NoulAnswer struct {
	Probability float64 `json:"probability"`
	Noul        float64 `json:"noul,omitempty"`
}

// UnmarshalJSON decodes noul/boolean answers supporting "noul", "probability", boolean "value", or raw primitive.
func (n *NoulAnswer) UnmarshalJSON(data []byte) error {
	var f float64
	if err := json.Unmarshal(data, &f); err == nil {
		n.Probability = f
		n.Noul = f
		return nil
	}
	var b bool
	if err := json.Unmarshal(data, &b); err == nil {
		if b {
			n.Probability = 1.0
			n.Noul = 1.0
		} else {
			n.Probability = 0.0
			n.Noul = 0.0
		}
		return nil
	}

	var aux struct {
		Probability *float64 `json:"probability"`
		Noul        *float64 `json:"noul"`
		Value       any      `json:"value"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	prob := 0.0
	if aux.Noul != nil {
		prob = *aux.Noul
	} else if aux.Probability != nil {
		prob = *aux.Probability
	} else if aux.Value != nil {
		switch v := aux.Value.(type) {
		case float64:
			prob = v
		case int:
			prob = float64(v)
		case bool:
			if v {
				prob = 1.0
			}
		}
	}
	n.Probability = prob
	n.Noul = prob
	return nil
}

// Value returns the evaluated proposition probability.
func (n *NoulAnswer) Value() float64 {
	if n == nil {
		return 0
	}
	if n.Probability != 0 {
		return n.Probability
	}
	return n.Noul
}

// DecisionResponse is the structured evaluation result from a decision model.
type DecisionResponse struct {
	// Raw answers map parsed from the provider.
	Answers map[string]any `json:"answers"`
	Usage   *Usage         `json:"usage,omitempty"`
	Latency int64          `json:"latency_ms,omitempty"`
	Model   string         `json:"model,omitempty"`
}

// GetChoice returns the ChoiceAnswer for a question if present and valid.
func (r *DecisionResponse) GetChoice(questionKey string) (*ChoiceAnswer, bool) {
	if r == nil || r.Answers == nil {
		return nil, false
	}
	raw, ok := r.Answers[questionKey]
	if !ok || raw == nil {
		return nil, false
	}

	normalize := func(ca *ChoiceAnswer) *ChoiceAnswer {
		if ca.Choice == "" && ca.Selected != "" {
			ca.Choice = ca.Selected
		} else if ca.Selected == "" && ca.Choice != "" {
			ca.Selected = ca.Choice
		}
		return ca
	}

	if ca, ok := raw.(*ChoiceAnswer); ok {
		return normalize(ca), true
	}
	if ca, ok := raw.(ChoiceAnswer); ok {
		return normalize(&ca), true
	}

	// Re-marshal map if decoded as map[string]any
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, false
	}
	var ca ChoiceAnswer
	if err := json.Unmarshal(data, &ca); err != nil {
		return nil, false
	}
	// Fallback to "value" field if choice and selected are empty
	if ca.Choice == "" && ca.Selected == "" {
		var alt struct {
			Value string `json:"value"`
		}
		if err := json.Unmarshal(data, &alt); err == nil && alt.Value != "" {
			ca.Choice = alt.Value
			ca.Selected = alt.Value
		}
	}
	return normalize(&ca), true
}

// GetScore returns the numeric score value for a question if present.
func (r *DecisionResponse) GetScore(questionKey string) (float64, bool) {
	if r == nil || r.Answers == nil {
		return 0, false
	}
	raw, ok := r.Answers[questionKey]
	if !ok || raw == nil {
		return 0, false
	}

	switch v := raw.(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case *ScoreAnswer:
		return v.Score, true
	case ScoreAnswer:
		return v.Score, true
	case map[string]any:
		if s, ok := v["score"].(float64); ok {
			return s, true
		}
		if s, ok := v["value"].(float64); ok {
			return s, true
		}
	}
	return 0, false
}

// GetNoul returns the probability (0..1) for a binary yes/no question.
func (r *DecisionResponse) GetNoul(questionKey string) (float64, bool) {
	if r == nil || r.Answers == nil {
		return 0, false
	}
	raw, ok := r.Answers[questionKey]
	if !ok || raw == nil {
		return 0, false
	}

	switch v := raw.(type) {
	case float64:
		return v, true
	case *NoulAnswer:
		return v.Value(), true
	case NoulAnswer:
		return v.Value(), true
	case map[string]any:
		if p, ok := v["noul"].(float64); ok {
			return p, true
		}
		if p, ok := v["probability"].(float64); ok {
			return p, true
		}
		if p, ok := v["value"].(float64); ok {
			return p, true
		}
		if b, ok := v["value"].(bool); ok {
			if b {
				return 1.0, true
			}
			return 0.0, true
		}
	}
	return 0, false
}

// DecisionProvider is the core interface implemented by System 1 decision models.
type DecisionProvider interface {
	// Evaluate evaluates a state against a schema of questions in a single forward pass.
	Evaluate(ctx context.Context, req DecisionRequest) (*DecisionResponse, error)

	// Name returns the provider identifier or user alias (e.g. "my-clef", "cloudflare-clef").
	Name() string

	// CanonicalName returns the underlying model engine name (e.g. "cloudflare-clef", "typesafe-jev", "openai-decision").
	CanonicalName() string

	// DefaultModel returns the default decision model ID.
	DefaultModel() string
}

// ValidateDecisionRequest validates that the request has a state and valid questions.
func ValidateDecisionRequest(req DecisionRequest) error {
	if req.State == "" {
		return fmt.Errorf("decision request state cannot be empty")
	}
	if len(req.Questions) == 0 {
		return fmt.Errorf("decision request must contain at least one question")
	}
	for key, q := range req.Questions {
		if !ValidDecisionQuestionTypes[q.Type] {
			return fmt.Errorf("invalid question type %q for question %q", q.Type, key)
		}
	}
	return nil
}
