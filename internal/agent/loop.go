package agent

import (
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

// runLoop (v2 agent iteration loop) was removed in the v3 force migration.
// All agents now use the v3 pipeline (runViaPipeline in loop_pipeline_adapter.go).
// Shared helpers below are still used by v3 pipeline callbacks.

// indexedResult holds the output of a single parallel tool execution, preserving
// the original call index so results can be sorted back into deterministic order.
type indexedResult struct {
	idx          int
	tc           providers.ToolCall
	registryName string
	result       *tools.Result
	argsJSON     string
	spanStart    time.Time
}

// resolveToolCallName strips the configured tool call prefix from a name
// returned by the model, returning the original registry name.
// Example: prefix "proxy_" + model calls "proxy_exec" → returns "exec".
func (l *Loop) resolveToolCallName(name string) string {
	if l.agentToolPolicy != nil && l.agentToolPolicy.ToolCallPrefix != "" {
		return tools.StripToolPrefix(l.agentToolPolicy.ToolCallPrefix, name)
	}
	return name
}

func (l *Loop) parallelEligibleToolCall(tc providers.ToolCall) bool {
	name := l.resolveToolCallName(tc.Name)
	switch {
	case name == "exec", name == "bash", name == "wait":
		return false
	case strings.HasPrefix(name, "mcp_"):
		return false
	case l.registry == nil:
		return false
	}
	tool, ok := l.registry.Get(name)
	if !ok {
		return false
	}

	meta := l.registry.GetMetadata(tool.Name())
	return meta.IsReadOnly() &&
		!meta.HasCapability(tools.CapMutating) &&
		!meta.HasCapability(tools.CapAsync) &&
		!meta.HasCapability(tools.CapMCPBridged)
}

// normalizeToolCall repairs known provider/model tool-call serialization
// failures before registry lookup. It recovers merged create_image fields and
// rewrites MCP pseudo-calls emitted as exec with {action:"mcp_xxx", ...}.
func (l *Loop) normalizeToolCall(tc providers.ToolCall) providers.ToolCall {
	tc = l.normalizeCreateImageToolCall(tc)

	if tc.Name != "exec" || len(tc.Arguments) == 0 {
		return tc
	}
	action, _ := tc.Arguments["action"].(string)
	if !strings.HasPrefix(action, "mcp_") {
		return tc
	}

	normalized := tc
	normalized.Name = action
	args := map[string]any{}
	if code, ok := tc.Arguments["code"]; ok {
		args["code"] = code
	} else if command, ok := tc.Arguments["command"]; ok {
		// Legacy prompt snippets sometimes place JS code in `command`.
		args["code"] = command
	}
	if len(args) == 0 {
		for k, v := range tc.Arguments {
			if k != "action" {
				args[k] = v
			}
		}
	}
	normalized.Arguments = args
	slog.Warn("tool call normalized from exec to mcp tool",
		"agent", l.id, "from", tc.Name, "to", normalized.Name)
	return normalized
}

func (l *Loop) normalizeCreateImageToolCall(tc providers.ToolCall) providers.ToolCall {
	if l.resolveToolCallName(tc.Name) == "create_image" {
		if args, sourceKey, ok := recoverMergedCreateImageArgs(tc.Arguments); ok {
			tc.Arguments = args
			slog.Warn("recovered merged create_image tool arguments",
				"agent", l.id, "source_key", sourceKey)
		}
	}
	return tc
}

// recoverMergedCreateImageArgs repairs a provider/model serialization failure
// where multiple XML-like tool fields are merged into a single filename string:
//
//	{"filename":"poster</filename_hint><prompt>...</prompt>..."}
//
// The payload is valid JSON, so provider parse-error guards cannot detect it.
// Keep recovery deliberately narrow: only act when prompt is absent and a
// complete filename_hint + prompt fingerprint is present.
func recoverMergedCreateImageArgs(args map[string]any) (map[string]any, string, bool) {
	if prompt, ok := args["prompt"].(string); ok && strings.TrimSpace(prompt) != "" {
		return args, "", false
	}

	var sourceKey, raw string
	for _, key := range []string{"filename_hint", "filename"} {
		value, ok := args[key].(string)
		if !ok || !strings.Contains(value, "</filename_hint>") {
			continue
		}
		sourceKey, raw = key, value
		break
	}
	if raw == "" {
		return args, "", false
	}

	hintEnd := strings.Index(raw, "</filename_hint>")
	hint := strings.TrimSpace(strings.TrimPrefix(raw[:hintEnd], "<filename_hint>"))
	mergedFields := raw[hintEnd+len("</filename_hint>"):]
	if strings.Count(mergedFields, "<prompt>") != 1 || strings.Count(mergedFields, "</prompt>") != 1 {
		return args, "", false
	}
	prompt, remainder, ok := extractMergedToolTag(mergedFields, "prompt")
	prompt = strings.TrimSpace(prompt)
	if !ok || prompt == "" {
		return args, "", false
	}

	repaired := make(map[string]any, len(args)+2)
	for key, value := range args {
		repaired[key] = value
	}
	delete(repaired, "filename")
	repaired["prompt"] = prompt
	if hint != "" {
		repaired["filename_hint"] = hint
	} else {
		delete(repaired, "filename_hint")
	}
	if _, explicit := repaired["ref_images"]; !explicit {
		if refs := extractMergedReferenceImages(remainder); len(refs) > 0 {
			repaired["ref_images"] = refs
		}
	}

	return repaired, sourceKey, true
}

func extractMergedToolTag(raw, tag string) (value, remainder string, ok bool) {
	open, close := "<"+tag+">", "</"+tag+">"
	start := strings.Index(raw, open)
	if start < 0 {
		return "", raw, false
	}
	valueStart := start + len(open)
	endOffset := strings.Index(raw[valueStart:], close)
	if endOffset < 0 {
		return "", raw, false
	}
	end := valueStart + endOffset
	return raw[valueStart:end], raw[end+len(close):], true
}

func extractMergedReferenceImages(raw string) []any {
	block, _, ok := extractMergedToolTag(raw, "ref_images")
	if !ok {
		return nil
	}

	items := extractAllMergedToolTags(block, "item")
	if len(items) == 0 {
		items = []string{block}
	}
	refs := make([]any, 0, len(items))
	for _, item := range items {
		ref := make(map[string]any)
		for _, key := range []string{"path", "url", "id", "description"} {
			if value, _, found := extractMergedToolTag(item, key); found {
				if value = strings.TrimSpace(value); value != "" {
					ref[key] = value
				}
			}
		}
		if value, _, found := extractMergedToolTag(item, "strength"); found {
			if strength, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil {
				ref["strength"] = strength
			}
		}
		if ref["path"] != nil || ref["url"] != nil || ref["id"] != nil {
			refs = append(refs, ref)
		}
	}
	return refs
}

func extractAllMergedToolTags(raw, tag string) []string {
	var values []string
	for raw != "" {
		value, remainder, ok := extractMergedToolTag(raw, tag)
		if !ok {
			break
		}
		values = append(values, value)
		raw = remainder
	}
	return values
}

func hasParseErrors(calls []providers.ToolCall) bool {
	for _, tc := range calls {
		if tc.ParseError != "" {
			return true
		}
	}
	return false
}

func truncateToolArgs(args map[string]any, maxLen int) map[string]any {
	out := make(map[string]any, len(args))
	for k, v := range args {
		if s, ok := v.(string); ok && len(s) > maxLen {
			out[k] = truncateStr(s, maxLen)
		} else {
			out[k] = v
		}
	}
	return out
}
