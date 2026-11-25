package http

import (
	"log/slog"
	"net/http"
	"slices"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/bootstrap"
	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/i18n"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

// maxAgentContextFileBytes caps the decoded content of an agent-level context file.
const maxAgentContextFileBytes = 256 << 10

// maxAgentContextFileBodyBytes caps the raw PUT body. JSON escaping can inflate
// content up to 6x (Go's encoder writes '<' as <), so the body cap must be
// well above the content cap or valid files fail with a misleading JSON error.
const maxAgentContextFileBodyBytes = 6*maxAgentContextFileBytes + 4<<10

// loadAgentForContextFiles resolves {id}, enforces tenant scope, and writes the
// error response itself. Returns nil when the request was already answered.
func (h *AgentsHandler) loadAgentForContextFiles(w http.ResponseWriter, r *http.Request) *store.AgentData {
	locale := store.LocaleFromContext(r.Context())
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, i18n.T(locale, i18n.MsgInvalidID, "agent"))
		return nil
	}
	ag, err := h.agents.GetByID(r.Context(), id)
	if err != nil || ag.TenantID != store.TenantIDFromContext(r.Context()) {
		writeError(w, http.StatusNotFound, protocol.ErrNotFound, i18n.T(locale, i18n.MsgNotFound, "agent", id.String()))
		return nil
	}
	return ag
}

// handleListAgentContextFiles: GET /v1/agents/{id}/files
func (h *AgentsHandler) handleListAgentContextFiles(w http.ResponseWriter, r *http.Request) {
	locale := store.LocaleFromContext(r.Context())
	ag := h.loadAgentForContextFiles(w, r)
	if ag == nil {
		return
	}
	dbFiles, err := h.agents.GetAgentContextFiles(r.Context(), ag.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, protocol.ErrInternal, i18n.T(locale, i18n.MsgFailedToList, "files"))
		return
	}
	sizes := make(map[string]int, len(dbFiles))
	for _, f := range dbFiles {
		sizes[f.FileName] = len(f.Content)
	}
	files := make([]map[string]any, 0, len(bootstrap.AgentEditableFiles))
	for _, name := range bootstrap.AgentEditableFiles {
		size, ok := sizes[name]
		entry := map[string]any{"name": name, "missing": !ok}
		if ok {
			entry["size"] = size
		}
		files = append(files, entry)
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent_key": ag.AgentKey, "files": files})
}

// handleGetAgentContextFile: GET /v1/agents/{id}/files/{name}
func (h *AgentsHandler) handleGetAgentContextFile(w http.ResponseWriter, r *http.Request) {
	locale := store.LocaleFromContext(r.Context())
	name := r.PathValue("name")
	if !slices.Contains(bootstrap.AgentEditableFiles, name) {
		writeError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, i18n.T(locale, i18n.MsgInvalidRequest, "file not allowed: "+name))
		return
	}
	ag := h.loadAgentForContextFiles(w, r)
	if ag == nil {
		return
	}
	dbFiles, err := h.agents.GetAgentContextFiles(r.Context(), ag.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, protocol.ErrInternal, i18n.T(locale, i18n.MsgFailedToList, "files"))
		return
	}
	for _, f := range dbFiles {
		if f.FileName == name {
			writeJSON(w, http.StatusOK, map[string]any{"name": name, "missing": false, "size": len(f.Content), "content": f.Content})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "missing": true})
}

// handleSetAgentContextFile: PUT /v1/agents/{id}/files/{name}  body {"content": "...", "propagate": false}
func (h *AgentsHandler) handleSetAgentContextFile(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxAgentContextFileBodyBytes)
	locale := store.LocaleFromContext(r.Context())
	name := r.PathValue("name")
	if !slices.Contains(bootstrap.AgentEditableFiles, name) {
		writeError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, i18n.T(locale, i18n.MsgInvalidRequest, "file not allowed: "+name))
		return
	}
	ag := h.loadAgentForContextFiles(w, r)
	if ag == nil {
		return
	}
	var body struct {
		Content   *string `json:"content"`
		Propagate bool    `json:"propagate"`
	}
	if !bindJSON(w, r, locale, &body) {
		return
	}
	if body.Content == nil {
		writeError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, i18n.T(locale, i18n.MsgRequired, "content"))
		return
	}
	if len(*body.Content) > maxAgentContextFileBytes {
		writeError(w, http.StatusRequestEntityTooLarge, protocol.ErrInvalidRequest, i18n.T(locale, i18n.MsgInvalidRequest, "content too large"))
		return
	}
	if err := h.agents.SetAgentContextFile(r.Context(), ag.ID, name, *body.Content); err != nil {
		writeError(w, http.StatusInternalServerError, protocol.ErrInternal, i18n.T(locale, i18n.MsgFailedToSave, "file", err.Error()))
		return
	}
	propagated := 0
	if body.Propagate {
		n, err := h.agents.PropagateContextFile(r.Context(), ag.ID, name)
		if err != nil {
			slog.Warn("agents.files.http.propagate_failed", "agent", ag.AgentKey, "file", name, "error", err)
		} else {
			propagated = n
		}
	}
	h.emitCacheInvalidate(bus.CacheKindAgent, ag.AgentKey)
	h.emitCacheInvalidate(bus.CacheKindBootstrap, ag.ID.String())
	emitAudit(h.msgBus, r, "agent.context_file.updated", "agent", ag.ID.String())
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "size": len(*body.Content), "propagated": propagated})
}
