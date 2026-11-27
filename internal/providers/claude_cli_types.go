package providers

import (
	"encoding/json"
	"strings"
)

// Claude CLI JSON response types (internal).
// These map to the output of `claude -p --output-format json/stream-json`.

// cliJSONResponse is the final result envelope from `--output-format json`.
type cliJSONResponse struct {
	Type          string    `json:"type"`       // "result"
	Subtype       string    `json:"subtype"`    // "success", "error"
	Result        string    `json:"result"`     // text response
	SessionID     string    `json:"session_id"` // CLI session UUID
	Model         string    `json:"model"`
	CostUSD       float64   `json:"cost_usd"`       // legacy CLI name
	TotalCostUSD  float64   `json:"total_cost_usd"` // current CLI name
	NumTurns      int       `json:"num_turns"`
	DurationAPIMS int       `json:"duration_api_ms"`
	Usage         *cliUsage `json:"usage"`
}

// cliUsage maps Claude CLI usage counters (Anthropic API shape): input_tokens
// excludes cached segments; cache read and creation are counted separately.
type cliUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
}

// toTraceUsage maps the CLI counters to Usage with cache fields set.
// Trace-only: ChatResponse.Usage keeps its input/output-only mapping so
// context/compaction/usage-cap accounting does not change.
func (u *cliUsage) toTraceUsage() Usage {
	if u == nil {
		return Usage{}
	}
	return Usage{
		PromptTokens:        u.InputTokens,
		CompletionTokens:    u.OutputTokens,
		TotalTokens:         u.InputTokens + u.OutputTokens,
		CacheCreationTokens: u.CacheCreationInputTokens,
		CacheReadTokens:     u.CacheReadInputTokens,
	}
}

// cliModelUsage is one entry of the result event's modelUsage map.
type cliModelUsage struct {
	InputTokens              int     `json:"inputTokens"`
	OutputTokens             int     `json:"outputTokens"`
	CacheReadInputTokens     int     `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int     `json:"cacheCreationInputTokens"`
	CostUSD                  float64 `json:"costUSD"`
}

// cliStreamEvent is a single line from `--output-format stream-json`.
type cliStreamEvent struct {
	Type    string        `json:"type"`              // "assistant", "user", "result", "system"
	Subtype string        `json:"subtype,omitempty"` // "success", "error", "init"
	Message *cliStreamMsg `json:"message,omitempty"` // for type="assistant" / "user"
	// ParentToolUseID is set on events produced inside a CLI Task subagent.
	ParentToolUseID string                   `json:"parent_tool_use_id,omitempty"`
	Result          string                   `json:"result,omitempty"` // for type="result"
	Model           string                   `json:"model,omitempty"`
	CostUSD         float64                  `json:"cost_usd,omitempty"`       // legacy CLI name
	TotalCostUSD    float64                  `json:"total_cost_usd,omitempty"` // current CLI name
	NumTurns        int                      `json:"num_turns,omitempty"`
	DurationAPIMS   int                      `json:"duration_api_ms,omitempty"`
	Usage           *cliUsage                `json:"usage,omitempty"`
	ModelUsage      map[string]cliModelUsage `json:"modelUsage,omitempty"`
	IsError         bool                     `json:"is_error,omitempty"` // true when subtype="error"
	Error           string                   `json:"error,omitempty"`    // error message (may be set when result is empty)
}

// cliCostUSD returns the reported cost, preferring the current field name.
func cliCostUSD(total, legacy float64) float64 {
	if total > 0 {
		return total
	}
	return legacy
}

// cliStreamMsg wraps content blocks inside an assistant or user message event.
type cliStreamMsg struct {
	ID         string            `json:"id,omitempty"` // API message id; shared by every event of one response
	Model      string            `json:"model,omitempty"`
	Role       string            `json:"role,omitempty"`
	StopReason string            `json:"stop_reason,omitempty"`
	Usage      *cliUsage         `json:"usage,omitempty"`
	Content    []cliContentBlock `json:"content"`
}

// UnmarshalJSON accepts messages whose content is a plain string (user turns).
func (m *cliStreamMsg) UnmarshalJSON(data []byte) error {
	type alias cliStreamMsg
	var raw struct {
		alias
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*m = cliStreamMsg(raw.alias)
	m.Content = nil
	if len(raw.Content) == 0 || raw.Content[0] != '[' {
		return nil // string or missing content: no blocks
	}
	return json.Unmarshal(raw.Content, &m.Content)
}

// cliContentBlock is a single content block (text, thinking, tool_use, tool_result).
type cliContentBlock struct {
	Type     string `json:"type"`               // "text", "thinking", "tool_use", "tool_result"
	Text     string `json:"text,omitempty"`     // for type="text"
	Thinking string `json:"thinking,omitempty"` // for type="thinking"

	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// tool_result
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"` // string or [{type:"text",text:...}]
	IsError   bool            `json:"is_error,omitempty"`
}

// toolResultText flattens a tool_result content payload to text.
func (b *cliContentBlock) toolResultText() string {
	if len(b.Content) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(b.Content, &s); err == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(b.Content, &parts); err != nil {
		return string(b.Content)
	}
	var sb strings.Builder
	for _, p := range parts {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		if p.Type == "text" {
			sb.WriteString(p.Text)
		} else {
			sb.WriteString("[" + p.Type + "]")
		}
	}
	return sb.String()
}
