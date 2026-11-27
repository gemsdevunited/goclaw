package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nextlevelbuilder/goclaw/internal/i18n"
	"github.com/nextlevelbuilder/goclaw/internal/providerresolve"
	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	usagecaps "github.com/nextlevelbuilder/goclaw/internal/usage/caps"
)

// IntentType represents the classified intent of a user message.
type IntentType string

const (
	IntentStatusQuery IntentType = "status_query"
	IntentCancel      IntentType = "cancel"
	IntentSteer       IntentType = "steer"
	IntentNewTask     IntentType = "new_task"
)

const intentClassifyTimeout = 10 * time.Second

const intentSystemPrompt = `You are an intent classifier. The user has sent a message while the AI assistant is busy processing a previous request.

Classify the user's intent into exactly ONE of these categories:
- status_query: The user is asking about progress, status, or what the assistant is currently doing (e.g., "what are you doing?", "status?", "how far along?", "đang làm gì?", "bao giờ xong?")
- cancel: The user wants to stop or cancel the current task (e.g., "stop", "cancel", "nevermind", "thôi", "dừng lại")
- steer: The user wants to add instructions or redirect the current task (e.g., "also check X", "focus on Y instead", "thêm phần Z nữa")
- new_task: The user is sending a new unrelated request or message

Respond with ONLY the category name, nothing else.`

// cancelKeywords for fast-path detection of obvious cancel intents.
// Only matched on very short messages (≤ 15 runes) to avoid false positives
// like "làm đơn giản thôi" matching "thôi".
var cancelKeywords = []string{
	"stop", "cancel", "abort", "thôi", "dừng", "hủy", "取消", "停",
	"nevermind", "never mind",
}

// exactCancelKeywords is a set of keywords that trigger immediate abort when
// sent as the entire message (exact match, case-insensitive, trimmed).
// Used by chat.send to auto-abort when user sends "stop" during an active run.
var exactCancelKeywords = map[string]bool{
	"stop": true, "cancel": true, "abort": true,
	"thôi": true, "dừng": true, "hủy": true,
	"取消": true, "停": true,
	"nevermind": true, "never mind": true,
}

// IsExactCancelKeyword returns true if the message is an exact cancel keyword
// (case-insensitive, whitespace-trimmed). Used by chat.send to auto-abort
// running agent loops when user explicitly sends a stop command.
func IsExactCancelKeyword(msg string) bool {
	return exactCancelKeywords[strings.ToLower(strings.TrimSpace(msg))]
}

// quickClassify attempts keyword-based classification for ultra-short messages
// before calling the LLM. Only messages ≤ 15 runes are fast-pathed; longer
// messages always go to LLM for proper context understanding.
// Cancel keywords require a whole-word match to avoid false positives (e.g. "nonstop").
// Returns (intent, true) if a match is found, or ("", false) to fall through to LLM.
func quickClassify(msg string) (IntentType, bool) {
	lower := strings.ToLower(strings.TrimSpace(msg))
	if utf8.RuneCountInString(lower) > 15 {
		return "", false
	}
	// Exact "?" → status query
	if lower == "?" {
		return IntentStatusQuery, true
	}
	for _, kw := range cancelKeywords {
		if containsWholeWord(lower, kw) {
			return IntentCancel, true
		}
	}
	return "", false
}

// containsWholeWord checks if s contains kw as a whole word (not a substring
// of a larger word). Word boundaries are: start/end of string, spaces, punctuation.
func containsWholeWord(s, kw string) bool {
	idx := strings.Index(s, kw)
	if idx < 0 {
		return false
	}
	// Check left boundary: must be start of string or non-letter
	if idx > 0 {
		r, _ := utf8.DecodeLastRuneInString(s[:idx])
		if isWordChar(r) {
			return false
		}
	}
	// Check right boundary: must be end of string or non-letter
	end := idx + len(kw)
	if end < len(s) {
		r, _ := utf8.DecodeRuneInString(s[end:])
		if isWordChar(r) {
			return false
		}
	}
	return true
}

// ClassifyIntent determines the intent of a user message sent while the agent is busy.
// Uses keyword fast-path first, then falls back to LLM classification.
// Falls back to IntentNewTask on any error.
func ClassifyIntent(ctx context.Context, provider providers.Provider, model, userMessage string) IntentType {
	return ClassifyIntentWithUsageCapsAndDecision(ctx, nil, nil, provider, model, userMessage)
}

func ClassifyIntentWithUsageCaps(ctx context.Context, usageCaps *usagecaps.Service, provider providers.Provider, model, userMessage string) IntentType {
	return ClassifyIntentWithUsageCapsAndDecision(ctx, nil, usageCaps, provider, model, userMessage)
}

// ErrDecisionFallback indicates the decision provider cannot confidently classify the intent
// and the caller should fall back to generative chat LLM.
var ErrDecisionFallback = errors.New("decision fallback to chat")

// ClassifyIntentWithDecision evaluates user intent using a System 1 DecisionProvider.
func ClassifyIntentWithDecision(ctx context.Context, decisionProv providers.DecisionProvider, userMessage string) (IntentType, bool) {
	intent, err := ClassifyIntentWithDecisionAndCaps(ctx, decisionProv, nil, userMessage)
	return intent, err == nil
}

// ClassifyIntentWithDecisionAndCaps evaluates user intent using a System 1 DecisionProvider,
// tracking tokens and enforcing budget caps via usageCaps when provided.
// Returns (intent, nil) on confident match, ErrCapExceeded on cap rejection,
// or ErrDecisionFallback if chat fallback should be attempted.
func ClassifyIntentWithDecisionAndCaps(ctx context.Context, decisionProv providers.DecisionProvider, usageCaps *usagecaps.Service, userMessage string) (IntentType, error) {
	if decisionProv == nil {
		return "", ErrDecisionFallback
	}

	req := providers.DecisionRequest{
		State: userMessage,
		Questions: map[string]providers.DecisionQuestion{
			"intent": {
				Type:         providers.QuestionTypeChoice,
				Instructions: "Classify user intent while an assistant is executing a task",
				Criteria: map[string]string{
					"status_query": "Asking about progress, status, or what the assistant is doing",
					"cancel":       "Asking to stop, abort, cancel, or nevermind",
					"steer":        "Adding instructions or redirecting the current task",
					"new_task":     "A new unrelated request or message",
				},
			},
		},
	}

	var resp *providers.DecisionResponse
	var err error
	if usageCaps != nil {
		resp, err = usageCaps.Evaluate(ctx, decisionProv, req, usagecaps.DecisionOptions{
			Purpose: "intent-classify",
		})
	} else {
		resp, err = decisionProv.Evaluate(ctx, req)
	}
	if err != nil {
		if errors.Is(err, usagecaps.ErrCapExceeded) {
			return "", err
		}
		if errors.Is(err, context.Canceled) {
			return "", err
		}
		return "", fmt.Errorf("%w: %v", ErrDecisionFallback, err)
	}
	if resp == nil {
		return "", fmt.Errorf("%w: nil response", ErrDecisionFallback)
	}

	ca, ok := resp.GetChoice("intent")
	if !ok || ca == nil {
		return "", fmt.Errorf("%w: missing choice in response", ErrDecisionFallback)
	}

	// Require confident classification (>= 0.60) and non-unknown value;
	// otherwise fall back to active generative LLM.
	if ca.Confidence < 0.60 {
		return "", fmt.Errorf("%w: low confidence (%.2f < 0.60)", ErrDecisionFallback, ca.Confidence)
	}

	switch ca.Value() {
	case "status_query":
		return IntentStatusQuery, nil
	case "cancel":
		return IntentCancel, nil
	case "steer":
		return IntentSteer, nil
	case "new_task":
		return IntentNewTask, nil
	default:
		return "", fmt.Errorf("%w: unknown choice value %q", ErrDecisionFallback, ca.Value())
	}
}

// ClassifyIntentWithUsageCapsAndDecision implements the 3-tier cascade:
// 1. Zero-cost deterministic keyword matching (0ms)
// 2. Fast System 1 DecisionProvider (Clef / Jev) (sub-timeout 2s, 0 completion tokens)
// 3. Fallback System 2 Generative LLM (remaining time of 10s deadline)
func ClassifyIntentWithUsageCapsAndDecision(ctx context.Context, decisionProv providers.DecisionProvider, usageCaps *usagecaps.Service, provider providers.Provider, model, userMessage string) IntentType {
	var cancel context.CancelFunc
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		ctx, cancel = context.WithTimeout(ctx, intentClassifyTimeout)
		defer cancel()
	}

	// Tier 1: Fast-path keyword matching for obvious patterns (zero cost, 0ms).
	if intent, ok := quickClassify(userMessage); ok {
		return intent
	}

	if ctx.Err() != nil {
		return IntentNewTask
	}

	// Tier 2: System 1 Decision Model.
	// Bound decision provider with a fast 2-second timeout (or remaining parent budget) to prevent stalling DM message queues.
	if decisionProv != nil {
		start := time.Now()
		tenantID := store.TenantIDFromContext(ctx)
		decTimeout := 2 * time.Second
		if dl, ok := ctx.Deadline(); ok {
			if remaining := time.Until(dl); remaining < decTimeout {
				decTimeout = remaining
			}
		}
		if decTimeout <= 0 {
			durationMs := time.Since(start).Milliseconds()
			slog.Info("decision: budget exhausted before call",
				"tenant_id", tenantID,
				"purpose", "intent-classify",
				"provider", decisionProv.Name(),
				"duration_ms", durationMs)
			return IntentNewTask
		}
		decCtx, decCancel := context.WithTimeout(ctx, decTimeout)
		intent, err := ClassifyIntentWithDecisionAndCaps(decCtx, decisionProv, usageCaps, userMessage)
		decCancel()
		durationMs := time.Since(start).Milliseconds()
		if err == nil {
			slog.Info("decision: evaluation succeeded",
				"tenant_id", tenantID,
				"purpose", "intent-classify",
				"provider", decisionProv.Name(),
				"intent", string(intent),
				"duration_ms", durationMs)
			return intent
		}
		// If usage cap rejected the request, do NOT bypass cap with chat fallback.
		if errors.Is(err, usagecaps.ErrCapExceeded) {
			slog.Warn("decision: usage cap blocked",
				"tenant_id", tenantID,
				"purpose", "intent-classify",
				"provider", decisionProv.Name(),
				"duration_ms", durationMs)
			return IntentNewTask
		}
		// If parent context was canceled or parent deadline exceeded, stop immediately.
		if ctx.Err() != nil {
			slog.Info("decision: parent context canceled, aborting cascade",
				"tenant_id", tenantID,
				"purpose", "intent-classify",
				"provider", decisionProv.Name(),
				"duration_ms", durationMs)
			return IntentNewTask
		}
		reason, cleanMsg := providerresolve.ClassifyDecisionError(err)
		slog.Info("decision: fallback to chat",
			"tenant_id", tenantID,
			"purpose", "intent-classify",
			"provider", decisionProv.Name(),
			"reason", reason,
			"duration_ms", durationMs,
			"error", cleanMsg)
	}

	// Tier 3: System 2 Generative LLM fallback (using remaining time of parent ctx).
	if ctx.Err() != nil {
		return IntentNewTask
	}
	if provider == nil {
		return IntentNewTask
	}

	req := providers.ChatRequest{
		Messages: []providers.Message{
			{Role: "system", Content: intentSystemPrompt},
			{Role: "user", Content: userMessage},
		},
		Model: model,
		Options: map[string]any{
			providers.OptMaxTokens:   20,
			providers.OptTemperature: 0.0,
		},
	}
	resp, err := usageCaps.Chat(ctx, provider, req, usagecaps.ChatOptions{
		ModelID:         model,
		Purpose:         "intent-classify",
		MaxOutputTokens: 20,
	})
	if err != nil {
		return IntentNewTask
	}

	result := strings.TrimSpace(strings.ToLower(resp.Content))
	switch {
	case strings.Contains(result, "status_query"):
		return IntentStatusQuery
	case strings.Contains(result, "cancel"):
		return IntentCancel
	case strings.Contains(result, "steer"):
		return IntentSteer
	default:
		return IntentNewTask
	}
}

// FormatStatusReply builds a user-friendly status response from the current agent activity.
func FormatStatusReply(status *AgentActivityStatus, locale string) string {
	if status == nil {
		return i18n.T(locale, i18n.MsgStatusWorking)
	}

	elapsed := time.Since(status.StartedAt).Round(time.Second)
	phase := formatPhase(status.Phase, status.Tool, locale)

	return i18n.T(locale, i18n.MsgStatusDetailed, phase, status.Iteration, elapsed)
}

// formatPhase returns a human-readable phase description.
func formatPhase(phase, tool, locale string) string {
	switch phase {
	case "thinking":
		return i18n.T(locale, i18n.MsgStatusPhaseThinking)
	case "tool_exec":
		if tool != "" {
			return i18n.T(locale, i18n.MsgStatusPhaseToolExec, formatToolLabel(tool))
		}
		return i18n.T(locale, i18n.MsgStatusPhaseTools)
	case "compacting":
		return i18n.T(locale, i18n.MsgStatusPhaseCompact)
	default:
		return i18n.T(locale, i18n.MsgStatusPhaseDefault)
	}
}

// formatToolLabel returns a user-friendly label for a tool name.
func formatToolLabel(tool string) string {
	switch {
	case strings.HasPrefix(tool, "web"):
		return "web search"
	case tool == "exec":
		return "code execution"
	case tool == "browser":
		return "browser"
	case tool == "spawn":
		return "delegation"
	case strings.HasPrefix(tool, "memory"):
		return "memory"
	case strings.HasPrefix(tool, "file"):
		return "file operations"
	default:
		return tool
	}
}
