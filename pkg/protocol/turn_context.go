package protocol

import "encoding/json"

// TurnContextVersion is the first supported client-supplied turn-context shape.
const TurnContextVersion = 1

// TurnContext carries optional, per-turn reference data for a chat request.
// It is intentionally separate from the user message and is never an authority
// for identity, tenancy, or access control.
//
// Data is free-form JSON — the server validates size and well-formedness but
// does not interpret the contents. Each client decides the internal structure.
type TurnContext struct {
	Version int             `json:"version"`
	Data    json.RawMessage `json:"data,omitempty"`
}
