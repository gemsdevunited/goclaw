package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

func TestRenderTurnContextLeavesMessageUntouchedWhenAbsent(t *testing.T) {
	if got := renderTurnContext("hello", nil); got != "hello" {
		t.Fatalf("renderTurnContext = %q", got)
	}
}

func TestRenderTurnContextAddsReferenceDataToUserTurn(t *testing.T) {
	got := renderTurnContext("answer the question", &protocol.TurnContext{
		Version: protocol.TurnContextVersion,
		Data:    json.RawMessage(`{"screen":"wiki","selectedDoc":"policy"}`),
	})
	if !strings.Contains(got, "<turn_context>") || !strings.HasPrefix(got, "answer the question") {
		t.Fatalf("rendered context = %q", got)
	}
}
