package agent

import (
	"reflect"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/providers"
)

func TestNormalizeToolCall_RepairsMergedCreateImageArguments(t *testing.T) {
	t.Parallel()

	raw := `police-tumbler-robin</filename_hint><prompt>A detailed police tumbler</prompt>` +
		`<ref_images><item><path>/app/workspace/.uploads/source.jpg</path></item></ref_images>`
	tc := providers.ToolCall{
		ID:        "call-image",
		Name:      "create_image",
		Arguments: map[string]any{"filename": raw},
	}

	got := (&Loop{}).normalizeToolCall(tc)
	if got.Arguments["prompt"] != "A detailed police tumbler" {
		t.Fatalf("prompt = %q", got.Arguments["prompt"])
	}
	if got.Arguments["filename_hint"] != "police-tumbler-robin" {
		t.Fatalf("filename_hint = %q", got.Arguments["filename_hint"])
	}
	if _, exists := got.Arguments["filename"]; exists {
		t.Fatal("malformed filename alias should be removed")
	}
	wantRefs := []any{map[string]any{"path": "/app/workspace/.uploads/source.jpg"}}
	if !reflect.DeepEqual(got.Arguments["ref_images"], wantRefs) {
		t.Fatalf("ref_images = %#v, want %#v", got.Arguments["ref_images"], wantRefs)
	}
}

func TestNormalizeToolCall_RepairsMergedCreateImageArgumentsWithoutReferences(t *testing.T) {
	t.Parallel()

	raw := `police-tumbler-robin</filename_hint><prompt>A detailed police tumbler</prompt>`
	tc := providers.ToolCall{
		ID:        "call-image-no-refs",
		Name:      "create_image",
		Arguments: map[string]any{"filename": raw},
	}

	got := (&Loop{}).normalizeToolCall(tc)
	if got.Arguments["prompt"] != "A detailed police tumbler" {
		t.Fatalf("prompt = %q", got.Arguments["prompt"])
	}
	if got.Arguments["filename_hint"] != "police-tumbler-robin" {
		t.Fatalf("filename_hint = %q", got.Arguments["filename_hint"])
	}
	if _, exists := got.Arguments["ref_images"]; exists {
		t.Fatalf("unexpected ref_images = %#v", got.Arguments["ref_images"])
	}
}

func TestNormalizeToolCall_DoesNotRewriteValidCreateImageArguments(t *testing.T) {
	t.Parallel()

	args := map[string]any{
		"prompt":        "Keep literal <prompt> markup in the design",
		"filename_hint": "poster",
	}
	tc := providers.ToolCall{ID: "call-valid", Name: "create_image", Arguments: args}

	got := (&Loop{}).normalizeToolCall(tc)
	if !reflect.DeepEqual(got.Arguments, args) {
		t.Fatalf("valid args changed: got %#v, want %#v", got.Arguments, args)
	}
}

func TestNormalizeToolCall_DoesNotGuessFromIncompleteMergedArguments(t *testing.T) {
	t.Parallel()

	args := map[string]any{"filename": `poster</filename_hint><prompt>missing close tag`}
	tc := providers.ToolCall{ID: "call-incomplete", Name: "create_image", Arguments: args}

	got := (&Loop{}).normalizeToolCall(tc)
	if !reflect.DeepEqual(got.Arguments, args) {
		t.Fatalf("incomplete args changed: got %#v, want %#v", got.Arguments, args)
	}
}

func TestNormalizeToolCall_DoesNotTruncateAmbiguousMergedPrompt(t *testing.T) {
	t.Parallel()

	args := map[string]any{
		"filename": `poster</filename_hint><prompt>Render literal </prompt> text</prompt>`,
	}
	tc := providers.ToolCall{ID: "call-ambiguous", Name: "create_image", Arguments: args}

	got := (&Loop{}).normalizeToolCall(tc)
	if !reflect.DeepEqual(got.Arguments, args) {
		t.Fatalf("ambiguous args changed: got %#v, want %#v", got.Arguments, args)
	}
}
