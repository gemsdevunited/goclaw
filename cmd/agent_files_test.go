package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadAgentFileContentFromFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "soul.md")
	if err := os.WriteFile(p, []byte("# Soul\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readAgentFileContent(p, nil, false)
	if err != nil || got != "# Soul\n" {
		t.Fatalf("got=%q err=%v", got, err)
	}

	empty := filepath.Join(dir, "empty.md")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readAgentFileContent(empty, nil, false); err == nil || !strings.Contains(err.Error(), "--allow-empty") {
		t.Fatalf("empty file must be refused, err=%v", err)
	}
	if got, err := readAgentFileContent(empty, nil, true); err != nil || got != "" {
		t.Fatalf("--allow-empty: got=%q err=%v", got, err)
	}
	if _, err := readAgentFileContent(filepath.Join(dir, "nope.md"), nil, false); err == nil {
		t.Fatal("missing file must error")
	}
}

func TestReadAgentFileContentFromPipe(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		w.WriteString("piped")
		w.Close()
	}()
	got, err := readAgentFileContent("", r, false)
	if err != nil || got != "piped" {
		t.Fatalf("got=%q err=%v", got, err)
	}

	// Closed, empty stdin (e.g. an agent exec without input) must not wipe the file.
	r2, w2, _ := os.Pipe()
	w2.Close()
	if _, err := readAgentFileContent("", r2, false); err == nil {
		t.Fatal("empty stdin must be refused")
	}
}

func TestResolveAgentIDByKeyAndUUID(t *testing.T) {
	const id = "019db3df-b7b3-7b01-a6c3-1e7d0ab357d3"
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/agents" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]any{"agents": []map[string]any{
			{"id": id, "agent_key": "goclaw-master"},
			{"id": "019dfb2c-2bff-72c5-b895-41259bd0d52f", "agent_key": "gemie"},
		}})
	}))
	defer srv.Close()

	oldServer, oldToken := gatewayServerOverride, gatewayTokenOverride
	t.Cleanup(func() { gatewayServerOverride, gatewayTokenOverride = oldServer, oldToken })
	gatewayServerOverride, gatewayTokenOverride = srv.URL, "t"

	got, err := resolveAgentID("goclaw-master")
	if err != nil || got != id {
		t.Fatalf("by key: got=%q err=%v", got, err)
	}
	if _, err := resolveAgentID("missing"); err == nil {
		t.Fatal("unknown key must error")
	}
	before := calls
	if got, err := resolveAgentID(id); err != nil || got != id || calls != before {
		t.Fatalf("UUID must pass through without a lookup: got=%q err=%v calls=%d", got, err, calls-before)
	}
}
