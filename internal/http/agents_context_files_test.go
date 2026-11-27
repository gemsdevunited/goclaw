package http

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/bootstrap"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

var (
	ctxFilesTenantA = uuid.MustParse("0193a5b0-7000-7000-8000-00000000000a")
	ctxFilesTenantB = uuid.MustParse("0193a5b0-7000-7000-8000-00000000000b")
)

type ctxFilesAgentStore struct {
	store.AgentStore
	agents       map[uuid.UUID]store.AgentData
	files        map[uuid.UUID]map[string]string
	propagated   []string
	propagateErr error
}

func newCtxFilesStore(agents ...store.AgentData) *ctxFilesAgentStore {
	s := &ctxFilesAgentStore{agents: map[uuid.UUID]store.AgentData{}, files: map[uuid.UUID]map[string]string{}}
	for _, a := range agents {
		s.agents[a.ID] = a
	}
	return s
}

func (s *ctxFilesAgentStore) GetByID(_ context.Context, id uuid.UUID) (*store.AgentData, error) {
	a, ok := s.agents[id]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return &a, nil
}

func (s *ctxFilesAgentStore) GetAgentContextFiles(_ context.Context, id uuid.UUID) ([]store.AgentContextFileData, error) {
	var out []store.AgentContextFileData
	for name, content := range s.files[id] {
		out = append(out, store.AgentContextFileData{AgentID: id, FileName: name, Content: content})
	}
	return out, nil
}

func (s *ctxFilesAgentStore) SetAgentContextFile(_ context.Context, id uuid.UUID, name, content string) error {
	if s.files[id] == nil {
		s.files[id] = map[string]string{}
	}
	s.files[id][name] = content
	return nil
}

func (s *ctxFilesAgentStore) PropagateContextFile(_ context.Context, _ uuid.UUID, name string) (int, error) {
	if s.propagateErr != nil {
		return 0, s.propagateErr
	}
	s.propagated = append(s.propagated, name)
	return 3, nil
}

func ctxFilesRequest(method, agentID, name, body string, tenant uuid.UUID) *http.Request {
	path := "/v1/agents/" + agentID + "/files"
	if name != "" {
		path += "/" + name
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.SetPathValue("id", agentID)
	if name != "" {
		req.SetPathValue("name", name)
	}
	ctx := store.WithTenantID(context.Background(), tenant)
	ctx = store.WithUserID(ctx, "admin")
	return req.WithContext(ctx)
}

func ctxFilesFixture(t *testing.T) (*AgentsHandler, *ctxFilesAgentStore, store.AgentData) {
	t.Helper()
	ag := store.AgentData{AgentKey: "gemie", TenantID: ctxFilesTenantA}
	ag.ID = uuid.New()
	s := newCtxFilesStore(ag)
	return &AgentsHandler{agents: s}, s, ag
}

func decodeBody(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode %q: %v", rr.Body.String(), err)
	}
	return m
}

func TestAgentContextFilesListReportsAllowedFiles(t *testing.T) {
	h, s, ag := ctxFilesFixture(t)
	s.files[ag.ID] = map[string]string{bootstrap.SoulFile: "soul!", "TOOLS.md": "hidden"}

	rr := httptest.NewRecorder()
	h.handleListAgentContextFiles(rr, ctxFilesRequest(http.MethodGet, ag.ID.String(), "", "", ctxFilesTenantA))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	files, _ := decodeBody(t, rr)["files"].([]any)
	if len(files) != len(bootstrap.AgentEditableFiles) {
		t.Fatalf("files=%d want %d", len(files), len(bootstrap.AgentEditableFiles))
	}
	for _, f := range files {
		m := f.(map[string]any)
		switch m["name"] {
		case bootstrap.SoulFile:
			if m["missing"] != false || m["size"] != float64(5) {
				t.Errorf("SOUL.md entry = %v", m)
			}
		case "TOOLS.md":
			t.Error("TOOLS.md must not be exposed")
		default:
			if m["missing"] != true {
				t.Errorf("%v should be missing: %v", m["name"], m)
			}
		}
	}
}

func TestAgentContextFilesGet(t *testing.T) {
	h, s, ag := ctxFilesFixture(t)
	s.files[ag.ID] = map[string]string{bootstrap.SoulFile: "be kind"}

	rr := httptest.NewRecorder()
	h.handleGetAgentContextFile(rr, ctxFilesRequest(http.MethodGet, ag.ID.String(), bootstrap.SoulFile, "", ctxFilesTenantA))
	if got := decodeBody(t, rr); rr.Code != http.StatusOK || got["content"] != "be kind" {
		t.Fatalf("status=%d body=%v", rr.Code, got)
	}

	rr = httptest.NewRecorder()
	h.handleGetAgentContextFile(rr, ctxFilesRequest(http.MethodGet, ag.ID.String(), bootstrap.IdentityFile, "", ctxFilesTenantA))
	if got := decodeBody(t, rr); rr.Code != http.StatusOK || got["missing"] != true {
		t.Fatalf("missing file: status=%d body=%v", rr.Code, got)
	}
}

func TestAgentContextFilesRejectsBadInput(t *testing.T) {
	h, _, ag := ctxFilesFixture(t)
	cases := []struct {
		name   string
		call   func(http.ResponseWriter, *http.Request)
		req    *http.Request
		status int
	}{
		{"get disallowed file", h.handleGetAgentContextFile,
			ctxFilesRequest(http.MethodGet, ag.ID.String(), "TOOLS.md", "", ctxFilesTenantA), http.StatusBadRequest},
		{"get path traversal", h.handleGetAgentContextFile,
			ctxFilesRequest(http.MethodGet, ag.ID.String(), "..%2Fetc%2Fpasswd", "", ctxFilesTenantA), http.StatusBadRequest},
		{"set disallowed file", h.handleSetAgentContextFile,
			ctxFilesRequest(http.MethodPut, ag.ID.String(), "TOOLS.md", `{"content":"x"}`, ctxFilesTenantA), http.StatusBadRequest},
		{"invalid uuid", h.handleListAgentContextFiles,
			ctxFilesRequest(http.MethodGet, "gemie", "", "", ctxFilesTenantA), http.StatusBadRequest},
		{"unknown agent", h.handleListAgentContextFiles,
			ctxFilesRequest(http.MethodGet, uuid.NewString(), "", "", ctxFilesTenantA), http.StatusNotFound},
		{"set without content field", h.handleSetAgentContextFile,
			ctxFilesRequest(http.MethodPut, ag.ID.String(), bootstrap.SoulFile, `{"propagate":true}`, ctxFilesTenantA), http.StatusBadRequest},
		{"set content too large", h.handleSetAgentContextFile,
			ctxFilesRequest(http.MethodPut, ag.ID.String(), bootstrap.SoulFile,
				`{"content":"`+strings.Repeat("a", maxAgentContextFileBytes+1)+`"}`, ctxFilesTenantA), http.StatusRequestEntityTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			tc.call(rr, tc.req)
			if rr.Code != tc.status {
				t.Fatalf("status=%d want %d body=%s", rr.Code, tc.status, rr.Body.String())
			}
		})
	}
}

// Agents of another tenant must look like they do not exist, for read and write.
func TestAgentContextFilesCrossTenantIsNotFound(t *testing.T) {
	h, s, ag := ctxFilesFixture(t)
	s.files[ag.ID] = map[string]string{bootstrap.SoulFile: "secret"}

	rr := httptest.NewRecorder()
	h.handleGetAgentContextFile(rr, ctxFilesRequest(http.MethodGet, ag.ID.String(), bootstrap.SoulFile, "", ctxFilesTenantB))
	if rr.Code != http.StatusNotFound || strings.Contains(rr.Body.String(), "secret") {
		t.Fatalf("cross-tenant get: status=%d body=%s", rr.Code, rr.Body.String())
	}

	rr = httptest.NewRecorder()
	h.handleSetAgentContextFile(rr, ctxFilesRequest(http.MethodPut, ag.ID.String(), bootstrap.SoulFile, `{"content":"pwned"}`, ctxFilesTenantB))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant set: status=%d", rr.Code)
	}
	if s.files[ag.ID][bootstrap.SoulFile] != "secret" {
		t.Fatal("cross-tenant write modified the file")
	}
}

func TestAgentContextFilesSetWritesAndPropagates(t *testing.T) {
	h, s, ag := ctxFilesFixture(t)

	rr := httptest.NewRecorder()
	h.handleSetAgentContextFile(rr, ctxFilesRequest(http.MethodPut, ag.ID.String(), bootstrap.SoulFile,
		`{"content":"new soul","propagate":true}`, ctxFilesTenantA))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if s.files[ag.ID][bootstrap.SoulFile] != "new soul" {
		t.Fatalf("stored=%q", s.files[ag.ID][bootstrap.SoulFile])
	}
	if got := decodeBody(t, rr); got["propagated"] != float64(3) {
		t.Fatalf("propagated=%v", got["propagated"])
	}

	// Empty content is a legitimate "clear" at the API level (CLI guards it).
	rr = httptest.NewRecorder()
	h.handleSetAgentContextFile(rr, ctxFilesRequest(http.MethodPut, ag.ID.String(), bootstrap.SoulFile, `{"content":""}`, ctxFilesTenantA))
	if rr.Code != http.StatusOK || s.files[ag.ID][bootstrap.SoulFile] != "" {
		t.Fatalf("clear: status=%d stored=%q", rr.Code, s.files[ag.ID][bootstrap.SoulFile])
	}
	if len(s.propagated) != 1 {
		t.Fatalf("propagate must only run when requested, calls=%v", s.propagated)
	}
}

func TestAgentContextFilesSetPropagateFailureStillSaves(t *testing.T) {
	h, s, ag := ctxFilesFixture(t)
	s.propagateErr = errors.New("db down")

	rr := httptest.NewRecorder()
	h.handleSetAgentContextFile(rr, ctxFilesRequest(http.MethodPut, ag.ID.String(), bootstrap.SoulFile,
		`{"content":"x","propagate":true}`, ctxFilesTenantA))
	if rr.Code != http.StatusOK || s.files[ag.ID][bootstrap.SoulFile] != "x" {
		t.Fatalf("status=%d stored=%q", rr.Code, s.files[ag.ID][bootstrap.SoulFile])
	}
	if got := decodeBody(t, rr); got["propagated"] != float64(0) {
		t.Fatalf("propagated=%v", got["propagated"])
	}
}

// Regression: Go's JSON encoder escapes '<' as < (6 bytes). A file just
// under the content cap made of HTML-ish markdown must still be accepted.
func TestAgentContextFilesSetAcceptsEscapeHeavyContentUnderCap(t *testing.T) {
	h, s, ag := ctxFilesFixture(t)
	content := strings.Repeat("<", maxAgentContextFileBytes)
	body, err := json.Marshal(map[string]any{"content": content})
	if err != nil {
		t.Fatal(err)
	}
	if len(body) <= maxAgentContextFileBytes+4<<10 {
		t.Fatalf("fixture does not exercise escaping: body=%d", len(body))
	}

	rr := httptest.NewRecorder()
	h.handleSetAgentContextFile(rr, ctxFilesRequest(http.MethodPut, ag.ID.String(), bootstrap.SoulFile, string(body), ctxFilesTenantA))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%.200s", rr.Code, rr.Body.String())
	}
	if len(s.files[ag.ID][bootstrap.SoulFile]) != maxAgentContextFileBytes {
		t.Fatalf("stored len=%d", len(s.files[ag.ID][bootstrap.SoulFile]))
	}
}
