package methods

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

const maxTurnContextBytes = 16 * 1024

// parseTurnContext applies the transport byte cap before JSON decoding. This
// prevents ignored fields or whitespace from bypassing the server's limit.
func parseTurnContext(raw json.RawMessage) (*protocol.TurnContext, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}
	if len(raw) > maxTurnContextBytes {
		return nil, fmt.Errorf("context is too large")
	}
	var input protocol.TurnContext
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, fmt.Errorf("invalid context JSON")
	}
	if input.Version != protocol.TurnContextVersion {
		return nil, fmt.Errorf("unsupported context version %d", input.Version)
	}
	if len(input.Data) > 0 && !json.Valid(input.Data) {
		return nil, fmt.Errorf("context data must be valid JSON")
	}
	return &input, nil
}
