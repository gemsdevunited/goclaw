package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestPrintAgentDetail(t *testing.T) {
	var buf bytes.Buffer
	printAgentDetail(&buf, map[string]any{
		"agent_key":         "gemie",
		"context_window":    float64(100000),
		"compaction_config": map[string]any{"maxHistoryShare": 0.6},
		"context_pruning":   map[string]any{},
	})
	out := buf.String()
	for _, want := range []string{
		"agent_key:",
		"gemie",
		"100000",
		`"maxHistoryShare": 0.6`,
		"context_pruning: (unset",
		"memory_config: (unset",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}
