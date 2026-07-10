package http

import (
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// UsageHandler serves pre-computed usage analytics from snapshots.
type UsageHandler struct {
	snapshots   store.SnapshotStore
	usageEvents store.UsageEventStore
	db          *sql.DB
}

func NewUsageHandler(snapshots store.SnapshotStore, usageEvents store.UsageEventStore, db *sql.DB) *UsageHandler {
	return &UsageHandler{snapshots: snapshots, usageEvents: usageEvents, db: db}
}

func (h *UsageHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/usage/timeseries", h.authMiddleware(h.handleTimeSeries))
	mux.HandleFunc("GET /v1/usage/breakdown", h.authMiddleware(h.handleBreakdown))
	mux.HandleFunc("GET /v1/usage/summary", h.authMiddleware(h.handleSummary))
	mux.HandleFunc("GET /v1/usage/events/timeseries", h.authMiddleware(h.handleEventTimeSeries))
	mux.HandleFunc("GET /v1/usage/events/breakdown", h.authMiddleware(h.handleEventBreakdown))
	mux.HandleFunc("GET /v1/usage/events/summary", h.authMiddleware(h.handleEventSummary))
	mux.HandleFunc("GET /v1/usage/members", h.authMiddleware(h.handleMemberUsage))
	mux.HandleFunc("GET /v1/usage/tools", h.authMiddleware(h.handleToolUsage))
}

func (h *UsageHandler) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return requireAuth("", next)
}

func (h *UsageHandler) handleTimeSeries(w http.ResponseWriter, r *http.Request) {
	q := parseSnapshotFilters(r)
	if q.From.IsZero() || q.To.IsZero() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "from and to are required"})
		return
	}
	if q.GroupBy == "" {
		q.GroupBy = "hour"
	}

	points, err := h.snapshots.GetTimeSeries(r.Context(), q)
	if err != nil {
		slog.Error("usage.timeseries query failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
		return
	}

	if from, to, ok := liveUsageWindow(q, time.Now().UTC()); ok {
		livePoint, err := h.queryLiveTimeSeries(r, from, to, q)
		if err != nil {
			slog.Warn("usage.timeseries live query failed", "error", err)
		} else if livePoint != nil {
			points = mergeSnapshotTimeSeries(points, *livePoint, q.GroupBy)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"points": points})
}

func (h *UsageHandler) handleBreakdown(w http.ResponseWriter, r *http.Request) {
	q := parseSnapshotFilters(r)
	if q.From.IsZero() || q.To.IsZero() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "from and to are required"})
		return
	}
	if q.GroupBy == "" {
		q.GroupBy = "provider"
	}

	rows, err := h.snapshots.GetBreakdown(r.Context(), q)
	if err != nil {
		slog.Error("usage.breakdown query failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
		return
	}
	if from, to, ok := liveUsageWindow(q, time.Now().UTC()); ok {
		liveRows, err := h.queryLiveBreakdown(r, from, to, q)
		if err != nil {
			slog.Warn("usage.breakdown live query failed", "error", err)
		} else {
			rows = mergeSnapshotBreakdowns(rows, liveRows, q.GroupBy)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": rows})
}

func (h *UsageHandler) handleSummary(w http.ResponseWriter, r *http.Request) {
	period := r.URL.Query().Get("period")
	if period == "" {
		period = "24h"
	}

	now := time.Now().UTC()
	var currentFrom, previousFrom time.Time

	switch period {
	case "today":
		currentFrom = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		previousFrom = currentFrom.AddDate(0, 0, -1)
	case "7d":
		currentFrom = now.Add(-7 * 24 * time.Hour)
		previousFrom = currentFrom.Add(-7 * 24 * time.Hour)
	case "30d":
		currentFrom = now.Add(-30 * 24 * time.Hour)
		previousFrom = currentFrom.Add(-30 * 24 * time.Hour)
	default: // "24h"
		currentFrom = now.Add(-24 * time.Hour)
		previousFrom = currentFrom.Add(-24 * time.Hour)
	}

	baseQ := parseSnapshotFilters(r)

	// Current period
	currentQ := baseQ
	currentQ.From = currentFrom
	currentQ.To = now
	currentQ.GroupBy = "hour"

	// Previous period (same duration, shifted back)
	previousQ := baseQ
	previousQ.From = previousFrom
	previousQ.To = currentFrom
	previousQ.GroupBy = "hour"

	currentSummary := h.aggregateTimeSeriesWithLive(r, currentQ, now)
	previousSummary := h.aggregateTimeSeries(r, previousQ)

	writeJSON(w, http.StatusOK, map[string]any{
		"current":  currentSummary,
		"previous": previousSummary,
	})
}

func (h *UsageHandler) handleEventTimeSeries(w http.ResponseWriter, r *http.Request) {
	if h.usageEvents == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "usage event analytics unavailable"})
		return
	}
	q, err := parseUsageEventFilters(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if q.From.IsZero() || q.To.IsZero() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "from and to are required"})
		return
	}
	if q.GroupBy == "" {
		q.GroupBy = "hour"
	}
	points, err := h.usageEvents.GetEventTimeSeries(r.Context(), q)
	if err != nil {
		slog.Error("usage.events.timeseries query failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"points": points})
}

func (h *UsageHandler) handleEventBreakdown(w http.ResponseWriter, r *http.Request) {
	if h.usageEvents == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "usage event analytics unavailable"})
		return
	}
	q, err := parseUsageEventFilters(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if q.From.IsZero() || q.To.IsZero() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "from and to are required"})
		return
	}
	if q.GroupBy == "" {
		q.GroupBy = "resource"
	}
	rows, err := h.usageEvents.GetEventBreakdown(r.Context(), q)
	if err != nil {
		slog.Error("usage.events.breakdown query failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": rows})
}

func (h *UsageHandler) handleEventSummary(w http.ResponseWriter, r *http.Request) {
	if h.usageEvents == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "usage event analytics unavailable"})
		return
	}
	q, err := parseUsageEventFilters(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if q.From.IsZero() || q.To.IsZero() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "from and to are required"})
		return
	}
	summary, err := h.usageEvents.GetEventSummary(r.Context(), q)
	if err != nil {
		slog.Error("usage.events.summary query failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"summary": summary})
}

func (h *UsageHandler) handleMemberUsage(w http.ResponseWriter, r *http.Request) {
	from, to := usageRange(r)
	if h.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "usage database unavailable"})
		return
	}
	tenantFilter, tenantArgs, next := tenantFilterForUsage(r, 3)
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT COALESCE(user_id, ''), COUNT(*), COALESCE(SUM(total_input_tokens), 0),
		       COALESCE(SUM(total_output_tokens), 0), COALESCE(SUM(total_cost), 0),
		       COUNT(*) FILTER (WHERE status IN ('error', 'cancelled')),
		       COALESCE(AVG(duration_ms), 0)
		FROM traces
		WHERE parent_trace_id IS NULL AND created_at >= $1 AND created_at < $2`+tenantFilter+`
		GROUP BY user_id ORDER BY SUM(total_input_tokens + total_output_tokens) DESC LIMIT 100`,
		append([]any{from, to}, tenantArgs...)...)
	_ = next
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "member usage query failed"})
		return
	}
	defer rows.Close()
	type memberRow struct {
		UserID        string  `json:"user_id"`
		Requests      int     `json:"requests"`
		InputTokens   int64   `json:"input_tokens"`
		OutputTokens  int64   `json:"output_tokens"`
		Cost          float64 `json:"cost_usd"`
		Errors        int     `json:"errors"`
		AvgDurationMS int     `json:"avg_duration_ms"`
	}
	result := make([]memberRow, 0, 100)
	for rows.Next() {
		var row memberRow
		if err := rows.Scan(&row.UserID, &row.Requests, &row.InputTokens, &row.OutputTokens, &row.Cost, &row.Errors, &row.AvgDurationMS); err != nil {
			continue
		}
		result = append(result, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": from, "to": to, "rows": result})
}

func (h *UsageHandler) handleToolUsage(w http.ResponseWriter, r *http.Request) {
	from, to := usageRange(r)
	if h.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "usage database unavailable"})
		return
	}
	tenantFilter, tenantArgs, next := tenantFilterForUsage(r, 3)
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT resource_type, resource_name, COALESCE(SUM(call_count), 0), COALESCE(SUM(error_count), 0),
		       COALESCE(SUM(total_tokens), 0), COALESCE(SUM(cost_usd), 0)
		FROM usage_events
		WHERE event_time >= $1 AND event_time < $2`+tenantFilter+`
		GROUP BY resource_type, resource_name ORDER BY SUM(call_count) DESC LIMIT 100`,
		append([]any{from, to}, tenantArgs...)...)
	_ = next
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "tool usage query failed"})
		return
	}
	defer rows.Close()
	type toolRow struct {
		ResourceType string  `json:"resource_type"`
		ResourceName string  `json:"resource_name"`
		Calls        int     `json:"calls"`
		Errors       int     `json:"errors"`
		TotalTokens  int64   `json:"total_tokens"`
		Cost         float64 `json:"cost_usd"`
	}
	result := make([]toolRow, 0, 100)
	for rows.Next() {
		var row toolRow
		if err := rows.Scan(&row.ResourceType, &row.ResourceName, &row.Calls, &row.Errors, &row.TotalTokens, &row.Cost); err != nil {
			continue
		}
		result = append(result, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": from, "to": to, "rows": result})
}

func usageRange(r *http.Request) (time.Time, time.Time) {
	now := time.Now().UTC()
	to := now
	from := now.Add(-30 * 24 * time.Hour)
	if value := r.URL.Query().Get("from"); value != "" {
		if parsed, err := time.Parse(time.RFC3339, value); err == nil {
			from = parsed
		}
	}
	if value := r.URL.Query().Get("to"); value != "" {
		if parsed, err := time.Parse(time.RFC3339, value); err == nil {
			to = parsed
		}
	}
	return from, to
}

func tenantFilterForUsage(r *http.Request, start int) (string, []any, int) {
	tid := store.TenantIDFromContext(r.Context())
	if tid == uuid.Nil {
		return " AND tenant_id = '00000000-0000-0000-0000-000000000000'", nil, start
	}
	return fmt.Sprintf(" AND tenant_id = $%d", start), []any{tid}, start + 1
}

// usageSummary is the response shape for summary endpoint.
type usageSummary struct {
	Requests      int     `json:"requests"`
	InputTokens   int64   `json:"input_tokens"`
	OutputTokens  int64   `json:"output_tokens"`
	Cost          float64 `json:"cost"`
	UniqueUsers   int     `json:"unique_users"`
	Errors        int     `json:"errors"`
	LLMCalls      int     `json:"llm_calls"`
	ToolCalls     int     `json:"tool_calls"`
	AvgDurationMS int     `json:"avg_duration_ms"`
}

func (h *UsageHandler) aggregateTimeSeries(r *http.Request, q store.SnapshotQuery) usageSummary {
	points, err := h.snapshots.GetTimeSeries(r.Context(), q)
	if err != nil {
		return usageSummary{}
	}

	return summarizeTimeSeries(points)
}

func parseSnapshotFilters(r *http.Request) store.SnapshotQuery {
	q := store.SnapshotQuery{}
	if v := r.URL.Query().Get("from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			q.From = t
		}
	}
	if v := r.URL.Query().Get("to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			q.To = t
		}
	}
	if v := r.URL.Query().Get("agent_id"); v != "" {
		if id, err := uuid.Parse(v); err == nil {
			q.AgentID = &id
		}
	}
	q.Provider = r.URL.Query().Get("provider")
	q.Model = r.URL.Query().Get("model")
	q.Channel = r.URL.Query().Get("channel")
	q.GroupBy = r.URL.Query().Get("group_by")
	return q
}

func parseUsageEventFilters(r *http.Request) (store.UsageEventQuery, error) {
	q := store.UsageEventQuery{}
	values := r.URL.Query()
	if values.Get("user_id") != "" {
		return q, fmt.Errorf("user_id filter is not supported")
	}
	if v := values.Get("from"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return q, fmt.Errorf("invalid from")
		}
		q.From = t
	}
	if v := values.Get("to"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return q, fmt.Errorf("invalid to")
		}
		q.To = t
	}
	if !q.From.IsZero() && !q.To.IsZero() && !q.To.After(q.From) {
		return q, fmt.Errorf("to must be after from")
	}
	if v := values.Get("agent_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			return q, fmt.Errorf("invalid agent_id")
		}
		q.AgentID = &id
	}
	q.Channel = values.Get("channel")
	q.EventType = values.Get("event_type")
	q.ResourceType = values.Get("resource_type")
	q.ResourceName = values.Get("resource_name")
	q.Provider = values.Get("provider")
	q.Model = values.Get("model")
	q.Status = values.Get("status")
	q.Source = values.Get("source")
	q.GroupBy = values.Get("group_by")
	if v := values.Get("limit"); v != "" {
		limit, err := strconv.Atoi(v)
		if err != nil || limit < 0 {
			return q, fmt.Errorf("invalid limit")
		}
		q.Limit = limit
	}
	return q, nil
}
