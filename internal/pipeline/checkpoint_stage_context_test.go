package pipeline

import (
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/providers"
)

func TestPersistableMessagesUsesCleanPersistedContent(t *testing.T) {
	clean := "clean user question"
	persisted := persistableMessages([]providers.Message{{
		Role:             "user",
		Content:          "<turn_context>secret context</turn_context>\n" + clean,
		PersistedContent: &clean,
	}})
	if len(persisted) != 1 || persisted[0].Content != clean {
		t.Fatalf("persisted = %#v", persisted)
	}
	if persisted[0].PersistedContent != nil {
		t.Fatal("persisted message must not retain the runtime-only override")
	}
}
