package http

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/agent"
	"github.com/nextlevelbuilder/goclaw/internal/i18n"
	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	"github.com/nextlevelbuilder/goclaw/internal/sessions"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

// WakeHandler handles POST /v1/agents/{id}/wake — external trigger API.
// Allows orchestrators (Paperclip, n8n, etc.) to trigger agent runs via HTTP.
type WakeHandler struct {
	agents   *agent.Router
	postTurn tools.PostTurnProcessor
}

// SetPostTurnProcessor sets the post-turn processor for team task dispatch.
func (h *WakeHandler) SetPostTurnProcessor(pt tools.PostTurnProcessor) {
	h.postTurn = pt
}

// NewWakeHandler creates a handler for the wake endpoint.
func NewWakeHandler(agents *agent.Router) *WakeHandler {
	return &WakeHandler{agents: agents}
}

// RegisterRoutes registers wake routes on the given mux.
func (h *WakeHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/agents/{id}/wake", h.handleWake)
}

type wakeRequest struct {
	Message    string         `json:"message"`
	SessionKey string         `json:"session_key,omitempty"`
	UserID     string         `json:"user_id,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	// Async triggers fire-and-forget execution: returns 202 immediately and
	// runs the agent in a background goroutine with a detached context.
	// Use this when the caller cannot keep the HTTP connection open long enough
	// for the agent to complete (e.g. Cloudflare Tunnel TTL constraints).
	Async bool `json:"async,omitempty"`
}

type wakeResponse struct {
	Content string   `json:"content"`
	RunID   string   `json:"run_id"`
	Usage   *wakeUsage `json:"usage,omitempty"`
}

type wakeUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func (h *WakeHandler) handleWake(w http.ResponseWriter, r *http.Request) {
	locale := extractLocale(r)

	// Auth + RBAC check (gateway token or API key, operator required for POST)
	auth := resolveAuth(r)
	if !auth.Authenticated {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": i18n.T(locale, i18n.MsgUnauthorized)})
		return
	}
	if !permissions.HasMinRole(auth.Role, permissions.RoleOperator) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": i18n.T(locale, i18n.MsgPermissionDenied, r.URL.Path)})
		return
	}

	// Inject tenant, role, user, and locale into context for downstream stores/tools.
	r = r.WithContext(enrichContext(r.Context(), r, auth))

	agentID := r.PathValue("id")
	if agentID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(locale, i18n.MsgInvalidID, "agent")})
		return
	}

	// Limit request body size
	const maxBodySize = 1 << 20 // 1MB
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)

	var req wakeRequest
	if !bindJSON(w, r, locale, &req) {
		return
	}

	if req.Message == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "message is required"})
		return
	}

	loop, err := h.agents.Get(r.Context(), agentID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": i18n.T(locale, i18n.MsgNotFound, "agent", agentID)})
		return
	}

	// Build session key
	sessionKey := req.SessionKey
	if sessionKey == "" {
		sessionKey = sessions.SessionKey(agentID, "wake-"+uuid.NewString()[:8])
	}

	// Body user_id override: allowed only when API key has no bound owner (prevents impersonation).
	userID := store.UserIDFromContext(r.Context())
	ctx := r.Context()
	if req.UserID != "" && req.UserID != userID {
		if auth.KeyData != nil && auth.KeyData.OwnerID != "" {
			slog.Warn("security.wake_owner_override_blocked",
				"req_user_id", req.UserID,
				"owner_id", auth.KeyData.OwnerID,
			)
		} else {
			userID = req.UserID
			ctx = store.WithUserID(ctx, req.UserID)
		}
	}

	runID := uuid.NewString()
	slog.Info("wake request", "agent", agentID, "user", userID, "session", sessionKey, "async", req.Async)

	// Async mode: return 202 immediately, run agent in background goroutine.
	// The HTTP connection is released before agent execution begins, so
	// reverse-proxy TTL constraints (e.g. Cloudflare Tunnel ~100s) won't
	// interrupt long-running agent tasks like AI code review.
	if req.Async {
		writeJSON(w, http.StatusAccepted, map[string]string{
			"status": "accepted",
			"run_id": runID,
			"session_key": sessionKey,
		})

		// Detach from the HTTP request context so the goroutine is not cancelled
		// when the connection closes. InjectTeamDispatch must be called inside
		// the goroutine using the new context.
		detachedCtx := context.WithoutCancel(ctx)
		postTurn := h.postTurn
		go func() {
			gCtx, drain := tools.InjectTeamDispatch(detachedCtx, postTurn)
			defer drain()
			_, runErr := loop.Run(gCtx, agent.RunRequest{
				SessionKey: sessionKey,
				Message:    req.Message,
				Channel:    "wake",
				ChatID:     "api",
				RunID:      runID,
				UserID:     userID,
				Stream:     false,
			})
			if runErr != nil {
				slog.Error("wake.async.run_failed", "agent", agentID, "run_id", runID, "error", runErr)
				return
			}
			slog.Info("wake.async.run_completed", "agent", agentID, "run_id", runID, "session", sessionKey)
		}()
		return
	}

	// Sync mode (default): block until agent completes.
	ctx, drainTeamDispatch := tools.InjectTeamDispatch(ctx, h.postTurn)
	defer drainTeamDispatch()

	result, err := loop.Run(ctx, agent.RunRequest{
		SessionKey: sessionKey,
		Message:    req.Message,
		Channel:    "wake",
		ChatID:     "api",
		RunID:      runID,
		UserID:     userID,
		Stream:     false,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("agent run failed: %v", err)})
		return
	}

	resp := wakeResponse{
		Content: result.Content,
		RunID:   runID,
	}
	if result.Usage != nil {
		resp.Usage = &wakeUsage{
			PromptTokens:     result.Usage.PromptTokens,
			CompletionTokens: result.Usage.CompletionTokens,
			TotalTokens:      result.Usage.TotalTokens,
		}
	}

	writeJSON(w, http.StatusOK, resp)
}
