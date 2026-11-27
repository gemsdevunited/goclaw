package providers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// parseJSONResponse parses the CLI JSON output into a ChatResponse.
// obs (may be nil) receives nested turn/tool callbacks replayed from the
// event array; see parseJSONArray.
func parseJSONResponse(data []byte, obs NestedTraceObserver, start time.Time) (*ChatResponse, error) {
	// Try parsing as JSON array first (CLI may output all events as a single array).
	if resp := parseJSONArray(data, obs, start); resp != nil {
		return resp, nil
	}

	// Fallback: CLI may output one JSON object per line.
	for line := range bytes.SplitSeq(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if resp := parseSingleJSONResult(line); resp != nil {
			return resp, nil
		}
	}

	// Last resort: treat entire output as text response
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil, fmt.Errorf("claude-cli: empty response")
	}
	return &ChatResponse{
		Content:      trimmed,
		FinishReason: "stop",
	}, nil
}

// parseJSONArray tries to parse data as a JSON array of CLI events, extracting
// the "result" event's text content and "assistant" event's text blocks.
//
// `--output-format json --verbose` prints every stream event in one array once
// the run ends. The events are replayed through cliTraceTracker so the trace
// gets the same turns, tool calls, tokens and cost as the stream-json path.
// Span times come from each event's "timestamp"; start (the process start)
// opens the first turn. Events without a timestamp fall back to parse time.
func parseJSONArray(data []byte, obs NestedTraceObserver, start time.Time) *ChatResponse {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil
	}

	var raws []json.RawMessage
	if err := json.Unmarshal(trimmed, &raws); err != nil {
		return nil
	}

	var resultText string
	var assistantText strings.Builder
	var usage *Usage
	resultIdx := -1
	finishReason := "stop"

	events := make([]cliStreamEvent, 0, len(raws))
	for _, raw := range raws {
		var ev cliStreamEvent
		if err := json.Unmarshal(raw, &ev); err != nil {
			continue
		}
		events = append(events, ev)

		switch ev.Type {
		case "result":
			resultText = ev.Result
			if ev.Subtype == "error" {
				finishReason = "error"
			}
			if ev.Usage != nil {
				usage = &Usage{
					PromptTokens:     ev.Usage.InputTokens,
					CompletionTokens: ev.Usage.OutputTokens,
					TotalTokens:      ev.Usage.InputTokens + ev.Usage.OutputTokens,
				}
			}
			resultIdx = len(events) - 1

		case "assistant":
			// Extract text from content blocks
			if ev.Message != nil {
				for _, block := range ev.Message.Content {
					if block.Type == "text" {
						assistantText.WriteString(block.Text)
					}
				}
			}
		}
	}

	// Prefer "result" text, fall back to concatenated assistant text blocks
	content := resultText
	if content == "" {
		content = assistantText.String()
	}
	if content == "" {
		return nil
	}

	// Replay only once the array is known to produce a response, so a
	// fallback parse never leaves orphan child spans behind.
	var resultEv *cliStreamEvent
	if resultIdx >= 0 {
		resultEv = &events[resultIdx]
	}
	tracker := newCLITraceTracker(obs, nil)
	if !start.IsZero() {
		tracker.runStart = start.UTC()
	}
	for i := range events {
		ev := &events[i]
		switch ev.Type {
		case "assistant":
			tracker.onAssistant(ev.Message, ev.ParentToolUseID, ev.Timestamp)
		case "user":
			tracker.onUser(ev.Message, ev.ParentToolUseID, ev.Timestamp)
		}
	}

	return &ChatResponse{
		Content:       content,
		FinishReason:  finishReason,
		Usage:         usage,
		ProviderTrace: tracker.finish(resultEv),
	}
}

// parseSingleJSONResult tries to parse a single JSON line as a "result" event.
func parseSingleJSONResult(line []byte) *ChatResponse {
	var resp cliJSONResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil
	}
	if resp.Type != "result" {
		return nil
	}
	cr := &ChatResponse{
		Content:      resp.Result,
		FinishReason: "stop",
	}
	if resp.Subtype == "error" {
		cr.FinishReason = "error"
	}
	if resp.Usage != nil {
		cr.Usage = &Usage{
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			TotalTokens:      resp.Usage.InputTokens + resp.Usage.OutputTokens,
		}
	}
	cr.ProviderTrace = &ProviderTrace{
		TotalUsage:    resp.Usage.toTraceUsage(),
		CostUSD:       cliCostUSD(resp.TotalCostUSD, resp.CostUSD),
		NumTurns:      resp.NumTurns,
		DurationAPIMS: resp.DurationAPIMS,
	}
	return cr
}

// extractStreamContent extracts text and thinking from a stream message.
func extractStreamContent(msg *cliStreamMsg) (text, thinking string) {
	var textBuf, thinkBuf strings.Builder
	for _, block := range msg.Content {
		switch block.Type {
		case "text":
			textBuf.WriteString(block.Text)
		case "thinking":
			thinkBuf.WriteString(block.Thinking)
		}
	}
	return textBuf.String(), thinkBuf.String()
}
