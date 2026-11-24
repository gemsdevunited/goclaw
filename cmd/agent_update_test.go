package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildAgentUpdateBody(t *testing.T) {
	cmd := agentUpdateCmd()
	patch := filepath.Join(t.TempDir(), "p.json")
	if err := os.WriteFile(patch, []byte(`{"model":"from-file","emoji":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, kv := range [][2]string{
		{"model", "m1"}, {"max-tokens", "0"}, {"json-file", patch},
	} {
		if err := cmd.Flags().Set(kv[0], kv[1]); err != nil {
			t.Fatal(err)
		}
	}
	_ = cmd.Flags().Set("set", `tools_config={"deny":["exec"]}`)
	_ = cmd.Flags().Set("set", `provider=minimax`) // bare word -> string

	o := agentUpdateOptions{Model: "m1", MaxTokens: 0, JSONFile: patch,
		SetPairs: []string{`tools_config={"deny":["exec"]}`, `provider=minimax`}}
	body, err := buildAgentUpdateBody(cmd, o)
	if err != nil {
		t.Fatal(err)
	}
	if body["model"] != "from-file" { // json-file overrides flags
		t.Errorf("model = %v", body["model"])
	}
	if v, ok := body["max_tokens"]; !ok || v != 0 {
		t.Errorf("explicit zero not sent: %v", body["max_tokens"])
	}
	if body["provider"] != "minimax" {
		t.Errorf("provider = %v", body["provider"])
	}
	tc, _ := body["tools_config"].(map[string]any)
	if tc == nil || tc["deny"] == nil {
		t.Errorf("tools_config = %v", body["tools_config"])
	}
	if _, ok := body["display_name"]; ok {
		t.Error("unchanged flag leaked into body")
	}
}

func TestBuildAgentUpdateBodyBadSet(t *testing.T) {
	if _, err := buildAgentUpdateBody(agentUpdateCmd(), agentUpdateOptions{SetPairs: []string{"noequals"}}); err == nil {
		t.Fatal("expected error")
	}
}

func TestBuildToolsSetBody(t *testing.T) {
	if _, err := buildToolsSetBody(false, true, ""); err == nil {
		t.Fatal("expected empty error")
	}
	b, err := buildToolsSetBody(true, false, `{"a":1}`)
	if err != nil || b["enabled"] != false || b["settings"] == nil {
		t.Fatalf("body=%v err=%v", b, err)
	}
	if _, err := buildToolsSetBody(false, false, `[1]`); err == nil {
		t.Fatal("expected non-object error")
	}
}
