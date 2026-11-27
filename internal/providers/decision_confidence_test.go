package providers

import (
	"encoding/json"
	"testing"
)

func TestChoiceAnswerConfidenceNeverDefaultsToCertain(t *testing.T) {
	var none ChoiceAnswer
	if err := json.Unmarshal([]byte(`{"choice":"cancel"}`), &none); err != nil {
		t.Fatal(err)
	}
	if none.Confidence != 0 {
		t.Fatalf("missing confidence must be 0, got %v", none.Confidence)
	}
	var fromProbs ChoiceAnswer
	if err := json.Unmarshal([]byte(`{"choice":"cancel","probabilities":{"cancel":0.8,"steer":0.2}}`), &fromProbs); err != nil {
		t.Fatal(err)
	}
	if fromProbs.Confidence != 0.8 {
		t.Fatalf("expected probability of choice, got %v", fromProbs.Confidence)
	}
}
