package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"
)

const fakeAgentID = "019dfb2c-2bff-72c5-b895-41259bd0d52f"

type recordedReq struct {
	Method, Path string
	Query        url.Values
	Body         map[string]any
	ContentType  string
	RawBody      []byte
}

// fakeGateway answers /health and /v1/agents, records every other request and
// replies with respond(path) or {"ok":true}.
type fakeGateway struct {
	mu      sync.Mutex
	reqs    []recordedReq
	respond func(r *http.Request) (int, string)
}

func newFakeGateway(t *testing.T) *fakeGateway {
	t.Helper()
	fg := &fakeGateway{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.WriteHeader(http.StatusOK)
			return
		case "/v1/agents":
			json.NewEncoder(w).Encode(map[string]any{"agents": []map[string]any{{"id": fakeAgentID, "agent_key": "gemie"}}})
			return
		}
		raw, _ := io.ReadAll(r.Body)
		rec := recordedReq{Method: r.Method, Path: r.URL.EscapedPath(), Query: r.URL.Query(),
			ContentType: r.Header.Get("Content-Type"), RawBody: raw}
		if strings.HasPrefix(rec.ContentType, "application/json") && len(raw) > 0 {
			_ = json.Unmarshal(raw, &rec.Body)
		}
		fg.mu.Lock()
		fg.reqs = append(fg.reqs, rec)
		fg.mu.Unlock()
		status, body := http.StatusOK, `{"ok":true}`
		if fg.respond != nil {
			status, body = fg.respond(r)
		}
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	oldServer, oldToken := gatewayServerOverride, gatewayTokenOverride
	t.Cleanup(func() { gatewayServerOverride, gatewayTokenOverride = oldServer, oldToken })
	gatewayServerOverride, gatewayTokenOverride = srv.URL, "test-token"
	return fg
}

func (fg *fakeGateway) only(t *testing.T) recordedReq {
	t.Helper()
	fg.mu.Lock()
	defer fg.mu.Unlock()
	if len(fg.reqs) != 1 {
		t.Fatalf("want exactly 1 data request, got %d: %+v", len(fg.reqs), fg.reqs)
	}
	return fg.reqs[0]
}

func (fg *fakeGateway) count() int {
	fg.mu.Lock()
	defer fg.mu.Unlock()
	return len(fg.reqs)
}

func runDataCmd(t *testing.T, root *cobra.Command, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(io.Discard)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const agentBase = "/v1/agents/" + fakeAgentID

func TestDataCommandsRequestShapes(t *testing.T) {
	soul := writeTemp(t, "note.md", "remember <this> & that")
	cases := []struct {
		name      string
		root      func() *cobra.Command
		args      []string
		method    string
		path      string
		query     map[string]string
		body      map[string]any
		noKeys    []string // body keys that must be absent
		noQueryOf []string // query keys that must be absent
	}{
		{name: "memory list", root: memoryCmd, args: []string{"list", "gemie", "--user", "u1"},
			method: "GET", path: agentBase + "/memory/documents", query: map[string]string{"user_id": "u1"}},
		{name: "memory get escapes segments", root: memoryCmd, args: []string{"get", "gemie", "notes/a b#1.md"},
			method: "GET", path: agentBase + "/memory/documents/notes/a%20b%231.md", noQueryOf: []string{"user_id"}},
		{name: "memory chunks", root: memoryCmd, args: []string{"chunks", "gemie", "notes/a.md"},
			method: "GET", path: agentBase + "/memory/chunks", query: map[string]string{"path": "notes/a.md"}},
		{name: "memory search", root: memoryCmd, args: []string{"search", "gemie", "deploy steps", "--max-results", "3"},
			method: "POST", path: agentBase + "/memory/search", body: map[string]any{"query": "deploy steps", "max_results": 3.0}},
		{name: "memory set", root: memoryCmd, args: []string{"set", "gemie", "notes/x.md", "--from-file", soul, "--user", "u2"},
			method: "PUT", path: agentBase + "/memory/documents/notes/x.md",
			body: map[string]any{"content": "remember <this> & that", "user_id": "u2"}},
		{name: "memory delete", root: memoryCmd, args: []string{"delete", "gemie", "notes/x.md", "--force"},
			method: "DELETE", path: agentBase + "/memory/documents/notes/x.md"},
		{name: "memory index-all", root: memoryCmd, args: []string{"index-all", "gemie"},
			method: "POST", path: agentBase + "/memory/index-all"},

		{name: "kg entities paging omits zero offset", root: kgCmd, args: []string{"entities", "gemie", "-q", "alice", "--limit", "5"},
			method: "GET", path: agentBase + "/kg/entities", query: map[string]string{"q": "alice", "limit": "5"},
			noQueryOf: []string{"offset", "type"}},
		{name: "kg get", root: kgCmd, args: []string{"get", "gemie", "ent-1"},
			method: "GET", path: agentBase + "/kg/entities/ent-1"},
		{name: "kg traverse", root: kgCmd, args: []string{"traverse", "gemie", "ent-1", "--depth", "3"},
			method: "POST", path: agentBase + "/kg/traverse", body: map[string]any{"entity_id": "ent-1", "max_depth": 3.0}},
		{name: "kg upsert", root: kgCmd, args: []string{"upsert", "gemie", "--external-id", "person:alice", "--name", "Alice",
			"--type", "person", "--prop", "role=CTO", "--prop", "team=core"},
			method: "POST", path: agentBase + "/kg/entities",
			body:   map[string]any{"external_id": "person:alice", "name": "Alice", "entity_type": "person"},
			noKeys: []string{"confidence", "user_id"}},
		{name: "kg merge", root: kgCmd, args: []string{"merge", "gemie", "--target", "a", "--source", "b", "--force"},
			method: "POST", path: agentBase + "/kg/merge", body: map[string]any{"target_id": "a", "source_id": "b"}},
		{name: "kg delete", root: kgCmd, args: []string{"delete", "gemie", "ent-1", "--force"},
			method: "DELETE", path: agentBase + "/kg/entities/ent-1"},
		{name: "kg dedup scan defaults", root: kgCmd, args: []string{"dedup", "scan", "gemie"},
			method: "POST", path: agentBase + "/kg/dedup/scan", body: map[string]any{"threshold": 0.9, "limit": 100.0}},
		{name: "kg dedup dismiss", root: kgCmd, args: []string{"dedup", "dismiss", "gemie", "cand-1"},
			method: "POST", path: agentBase + "/kg/dedup/dismiss", body: map[string]any{"candidate_id": "cand-1"}},
		{name: "kg extract", root: kgCmd, args: []string{"extract", "gemie", "--provider", "p", "--model", "m", "--text", "Alice leads Core"},
			method: "POST", path: agentBase + "/kg/extract", body: map[string]any{"text": "Alice leads Core", "provider": "p", "model": "m"}},

		{name: "vault list tenant-wide", root: vaultCmd, args: []string{"list", "--doc-type", "note"},
			method: "GET", path: "/v1/vault/documents", query: map[string]string{"doc_type": "note"}},
		{name: "vault search agent-scoped", root: vaultCmd, args: []string{"search", "roadmap", "--agent", "gemie", "--doc-type", "note,memory"},
			method: "POST", path: agentBase + "/vault/search", body: map[string]any{"query": "roadmap", "doc_types": []any{"note", "memory"}}},
		{name: "vault tree resolves agent to uuid", root: vaultCmd, args: []string{"tree", "--agent", "gemie", "--path", "notes"},
			method: "GET", path: "/v1/vault/tree", query: map[string]string{"agent_id": fakeAgentID, "path": "notes"}},
		{name: "vault create with content", root: vaultCmd, args: []string{"create", "--path", "notes/n.md", "--title", "N", "--from-file", soul},
			method: "POST", path: "/v1/vault/documents", body: map[string]any{"path": "notes/n.md", "title": "N", "content": "remember <this> & that"}},
		{name: "vault create without content omits field", root: vaultCmd, args: []string{"create", "--path", "notes/n.md", "--title", "N"},
			method: "POST", path: "/v1/vault/documents", noKeys: []string{"content"}},
		{name: "vault update sends only changed", root: vaultCmd, args: []string{"update", "doc-1", "--title", "New"},
			method: "PUT", path: "/v1/vault/documents/doc-1", body: map[string]any{"title": "New"},
			noKeys: []string{"scope", "doc_type", "team_id", "content"}},
		{name: "vault update can clear team", root: vaultCmd, args: []string{"update", "doc-1", "--team", ""},
			method: "PUT", path: "/v1/vault/documents/doc-1", body: map[string]any{"team_id": ""}},
		{name: "vault link", root: vaultCmd, args: []string{"link", "--from", "d1", "--to", "d2", "--agent", "gemie"},
			method: "POST", path: agentBase + "/vault/links", body: map[string]any{"from_doc_id": "d1", "to_doc_id": "d2"}},
		{name: "vault unlink", root: vaultCmd, args: []string{"unlink", "l1", "--force"},
			method: "DELETE", path: "/v1/vault/links/l1"},
		{name: "vault delete", root: vaultCmd, args: []string{"delete", "doc-1", "--force", "--agent", "gemie"},
			method: "DELETE", path: agentBase + "/vault/documents/doc-1"},

		{name: "storage list", root: storageCmd, args: []string{"list", "--path", "agents"},
			method: "GET", path: "/v1/storage/files", query: map[string]string{"path": "agents"}, noQueryOf: []string{"depth"}},
		{name: "storage get escapes", root: storageCmd, args: []string{"get", "a/b c.txt"},
			method: "GET", path: "/v1/storage/files/a/b%20c.txt"},
		{name: "storage move", root: storageCmd, args: []string{"move", "a.txt", "b/a.txt"},
			method: "PUT", path: "/v1/storage/move", query: map[string]string{"from": "a.txt", "to": "b/a.txt"}},
		{name: "storage delete", root: storageCmd, args: []string{"delete", "tmp/x", "--force"},
			method: "DELETE", path: "/v1/storage/files/tmp/x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fg := newFakeGateway(t)
			if _, err := runDataCmd(t, tc.root(), tc.args...); err != nil {
				t.Fatalf("run: %v", err)
			}
			got := fg.only(t)
			if got.Method != tc.method || got.Path != tc.path {
				t.Fatalf("request = %s %s, want %s %s", got.Method, got.Path, tc.method, tc.path)
			}
			for k, v := range tc.query {
				if got.Query.Get(k) != v {
					t.Errorf("query %s = %q, want %q (all: %v)", k, got.Query.Get(k), v, got.Query)
				}
			}
			for _, k := range tc.noQueryOf {
				if got.Query.Has(k) {
					t.Errorf("query %s must be omitted, got %q", k, got.Query.Get(k))
				}
			}
			for k, want := range tc.body {
				gotJSON, _ := json.Marshal(got.Body[k])
				wantJSON, _ := json.Marshal(want)
				if !bytes.Equal(gotJSON, wantJSON) {
					t.Errorf("body[%s] = %s, want %s", k, gotJSON, wantJSON)
				}
			}
			for _, k := range tc.noKeys {
				if _, ok := got.Body[k]; ok {
					t.Errorf("body key %s must be absent: %v", k, got.Body)
				}
			}
		})
	}
}

func TestKGUpsertSendsProperties(t *testing.T) {
	fg := newFakeGateway(t)
	if _, err := runDataCmd(t, kgCmd(), "upsert", "gemie", "--external-id", "x", "--name", "X", "--type", "t",
		"--prop", "role=CTO", "--prop", "url=a=b"); err != nil {
		t.Fatal(err)
	}
	props, _ := fg.only(t).Body["properties"].(map[string]any)
	if props["role"] != "CTO" || props["url"] != "a=b" {
		t.Fatalf("properties = %v", props)
	}
}

// Destructive commands must refuse without --force and must not touch the API.
func TestDestructiveCommandsRequireForce(t *testing.T) {
	cases := []struct {
		root func() *cobra.Command
		args []string
	}{
		{memoryCmd, []string{"delete", "gemie", "notes/x.md"}},
		{kgCmd, []string{"delete", "gemie", "ent-1"}},
		{kgCmd, []string{"merge", "gemie", "--target", "a", "--source", "b"}},
		{vaultCmd, []string{"delete", "doc-1"}},
		{vaultCmd, []string{"unlink", "l1"}},
		{storageCmd, []string{"delete", "tmp/x"}},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			fg := newFakeGateway(t)
			_, err := runDataCmd(t, tc.root(), tc.args...)
			if !errors.Is(err, errForceRequired) {
				t.Fatalf("err = %v, want errForceRequired", err)
			}
			if fg.count() != 0 {
				t.Fatalf("sent %d requests without --force", fg.count())
			}
		})
	}
}

func TestDataCommandsValidateBeforeSending(t *testing.T) {
	cases := []struct {
		root func() *cobra.Command
		args []string
		want string
	}{
		{kgCmd, []string{"merge", "gemie", "--target", "a", "--source", "a", "--force"}, "must differ"},
		{kgCmd, []string{"merge", "gemie", "--force"}, "--target and --source"},
		{kgCmd, []string{"upsert", "gemie", "--name", "X", "--type", "t"}, "external_id is required"},
		{kgCmd, []string{"upsert", "gemie", "--external-id", "x", "--name", "X", "--type", "t", "--prop", "bad"}, "invalid --prop"},
		{kgCmd, []string{"extract", "gemie", "--text", "hi"}, "--provider and --model"},
		{vaultCmd, []string{"create", "--title", "N"}, "--path and --title"},
		{vaultCmd, []string{"update", "doc-1"}, "nothing to update"},
		{vaultCmd, []string{"link", "--from", "d1"}, "--from and --to"},
		{memoryCmd, []string{"search", "nobody", "q"}, `agent "nobody" not found`},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			fg := newFakeGateway(t)
			_, err := runDataCmd(t, tc.root(), tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
			if fg.count() != 0 {
				t.Fatalf("sent %d requests for invalid input", fg.count())
			}
		})
	}
}

func TestDataCommandsPrintArraysAndSurfaceErrors(t *testing.T) {
	fg := newFakeGateway(t)
	fg.respond = func(*http.Request) (int, string) { return 200, `[{"path":"a.md"},{"path":"b.md"}]` }
	out, err := runDataCmd(t, memoryCmd(), "list", "gemie")
	if err != nil {
		t.Fatalf("array response must be accepted: %v", err)
	}
	var docs []map[string]any
	if err := json.Unmarshal([]byte(out), &docs); err != nil || len(docs) != 2 {
		t.Fatalf("output %q not the JSON array: %v", out, err)
	}

	fg2 := newFakeGateway(t)
	fg2.respond = func(*http.Request) (int, string) { return 403, `{"error":"tenant admin required"}` }
	_, err = runDataCmd(t, storageCmd(), "delete", "x", "--force")
	if err == nil || !strings.Contains(err.Error(), "tenant admin required") {
		t.Fatalf("server error not surfaced: %v", err)
	}
}

func TestStorageSizeReadsLastSSEEvent(t *testing.T) {
	fg := newFakeGateway(t)
	fg.respond = func(*http.Request) (int, string) {
		return 200, "data: {\"current\":10,\"files\":1}\n\ndata: {\"total\":42,\"files\":3,\"done\":true}\n\n"
	}
	out, err := runDataCmd(t, storageCmd(), "size")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil || got["total"] != 42.0 || got["done"] != true {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestStorageGetRawWritesBytes(t *testing.T) {
	fg := newFakeGateway(t)
	fg.respond = func(*http.Request) (int, string) { return 200, "\x89PNG raw" }
	out, err := runDataCmd(t, storageCmd(), "get", "img.png", "--raw")
	if err != nil {
		t.Fatal(err)
	}
	if out != "\x89PNG raw" || fg.only(t).Query.Get("raw") != "true" {
		t.Fatalf("out=%q query=%v", out, fg.only(t).Query)
	}
}

func TestStorageUploadSendsMultipartFile(t *testing.T) {
	fg := newFakeGateway(t)
	local := writeTemp(t, "report.csv", "a,b\n1,2\n")
	if _, err := runDataCmd(t, storageCmd(), "upload", local, "--path", "reports"); err != nil {
		t.Fatal(err)
	}
	got := fg.only(t)
	if got.Method != "POST" || got.Path != "/v1/storage/files" || got.Query.Get("path") != "reports" {
		t.Fatalf("request = %s %s %v", got.Method, got.Path, got.Query)
	}
	req, _ := http.NewRequest("POST", "/", bytes.NewReader(got.RawBody))
	req.Header.Set("Content-Type", got.ContentType)
	file, header, err := req.FormFile("file")
	if err != nil {
		t.Fatalf("multipart field 'file': %v", err)
	}
	data, _ := io.ReadAll(file)
	if header.Filename != "report.csv" || string(data) != "a,b\n1,2\n" {
		t.Fatalf("filename=%q data=%q", header.Filename, data)
	}
}

func TestDataHelpers(t *testing.T) {
	if got := withQuery("/p", "a", "1", "b", "", "c", "x y"); got != "/p?a=1&c=x+y" {
		t.Errorf("withQuery = %q", got)
	}
	if got := withQuery("/p", "a", ""); got != "/p" {
		t.Errorf("withQuery empty = %q", got)
	}
	if got := escapePathSegments("/dir/a b/%x.md"); got != "dir/a%20b/%25x.md" {
		t.Errorf("escapePathSegments = %q", got)
	}
	if _, err := lastSSEData([]byte("event: ping\n\n")); err == nil {
		t.Error("lastSSEData must fail without data lines")
	}
}
