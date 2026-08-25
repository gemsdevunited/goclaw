package tools

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/providers"
)

// TestCreateImageTool_AutoAttachesLastAssistantImage verifies the safety-net
// branch in resolveReferenceImages: when the LLM omits ref_images and no
// current-turn upload is present, the most recent assistant image is
// auto-attached as a reference with the default strength.
func TestCreateImageTool_AutoAttachesLastAssistantImage(t *testing.T) {
	tmpDir := t.TempDir()
	refFile := filepath.Join(tmpDir, "last-assistant.png")
	pngBytes := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	if err := os.WriteFile(refFile, pngBytes, 0644); err != nil {
		t.Fatalf("failed to write prior image: %v", err)
	}

	fakeProvider := &nativeImageProvider{
		name:       "openai-codex",
		model:      "gpt-image-2",
		returnData: pngBytes,
	}
	reg := providers.NewRegistry(nil)
	reg.Register(fakeProvider)

	ctx := WithToolWorkspace(context.Background(), tmpDir)
	ctx = WithLastAssistantImage(ctx, &providers.MediaRef{
		ID:       "last-img",
		Kind:     "image",
		MimeType: "image/png",
		Path:     refFile,
		Prompt:   "previous turn prompt",
	})
	ctx = WithBuiltinToolSettings(ctx, BuiltinToolSettings{
		"create_image": []byte(`{"providers":[{"provider":"openai-codex","model":"gpt-image-2","enabled":true,"timeout":30,"max_retries":1}]}`),
	})

	result := NewCreateImageTool(reg).Execute(ctx, map[string]any{
		"prompt":       "add a black cat next to the ghost",
		"aspect_ratio": "1:1",
	})
	if result.IsError {
		t.Fatalf("Execute returned error: %q", result.ForLLM)
	}
	if fakeProvider.calledWith == nil {
		t.Fatal("GenerateImage was not called on the native provider")
	}

	if len(fakeProvider.calledWith.RefImages) != 1 {
		t.Fatalf("expected 1 auto-attached reference image, got %d", len(fakeProvider.calledWith.RefImages))
	}
	if fakeProvider.calledWith.RefImages[0].Base64 == "" {
		t.Error("RefImages[0].Base64 was not populated from the prior image")
	}
	if fakeProvider.calledWith.RefImages[0].Strength != defaultAutoAttachStrength {
		t.Errorf("RefImages[0].Strength = %f, want %f",
			fakeProvider.calledWith.RefImages[0].Strength, defaultAutoAttachStrength)
	}
	// Description is internal to resolveReferenceImages and is not surfaced
	// through providers.RefImage (which is the wire format). The strength
	// assertion above is the meaningful behavioural check.
}

// TestCreateImageTool_ExplicitRefImagesWinsOverAutoAttach verifies that when
// the LLM passes ref_images explicitly, the auto-attach of the last assistant
// image is bypassed — the explicit list is used as-is. This preserves the
// LLM-as-primary-decider design.
func TestCreateImageTool_ExplicitRefImagesWinsOverAutoAttach(t *testing.T) {
	tmpDir := t.TempDir()
	explicitFile := filepath.Join(tmpDir, "explicit.png")
	lastAssistantFile := filepath.Join(tmpDir, "last-assistant.png")
	pngBytes := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	if err := os.WriteFile(explicitFile, pngBytes, 0644); err != nil {
		t.Fatalf("failed to write explicit image: %v", err)
	}
	if err := os.WriteFile(lastAssistantFile, pngBytes, 0644); err != nil {
		t.Fatalf("failed to write last assistant image: %v", err)
	}

	fakeProvider := &nativeImageProvider{
		name:       "openai-codex",
		model:      "gpt-image-2",
		returnData: pngBytes,
	}
	reg := providers.NewRegistry(nil)
	reg.Register(fakeProvider)

	ctx := WithToolWorkspace(context.Background(), tmpDir)
	ctx = WithLastAssistantImage(ctx, &providers.MediaRef{
		ID:       "last",
		Kind:     "image",
		MimeType: "image/png",
		Path:     lastAssistantFile,
	})
	ctx = WithBuiltinToolSettings(ctx, BuiltinToolSettings{
		"create_image": []byte(`{"providers":[{"provider":"openai-codex","model":"gpt-image-2","enabled":true,"timeout":30,"max_retries":1}]}`),
	})

	result := NewCreateImageTool(reg).Execute(ctx, map[string]any{
		"prompt": "edit this other image",
		"ref_images": []any{
			map[string]any{"path": explicitFile, "strength": 0.3},
		},
	})
	if result.IsError {
		t.Fatalf("Execute returned error: %q", result.ForLLM)
	}
	if len(fakeProvider.calledWith.RefImages) != 1 {
		t.Fatalf("expected 1 explicit reference image, got %d", len(fakeProvider.calledWith.RefImages))
	}
	if fakeProvider.calledWith.RefImages[0].Strength != 0.3 {
		t.Errorf("RefImages[0].Strength = %f, want 0.3 (from explicit ref)",
			fakeProvider.calledWith.RefImages[0].Strength)
	}
	if len(fakeProvider.calledWith.RefImages) == 1 {
		// The auto-attach should NOT have fired — verify by checking the
		// last assistant file was not loaded into the first ref's data.
		// If both were loaded, the result would still pass the count test;
		// the strength assertion above is the differentiator since auto-attach
		// uses defaultAutoAttachStrength (0.6) while explicit was 0.3.
	}
}

// TestCreateImageTool_UseCurrentImagesFalseDisablesAutoAttach verifies the
// opt-out path: use_current_images=false must suppress both current-turn
// auto-attach AND the new last-assistant-image auto-attach. The LLM uses
// this to signal a brand-new independent image unrelated to session history.
func TestCreateImageTool_UseCurrentImagesFalseDisablesAutoAttach(t *testing.T) {
	tmpDir := t.TempDir()
	lastAssistantFile := filepath.Join(tmpDir, "last-assistant.png")
	pngBytes := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	if err := os.WriteFile(lastAssistantFile, pngBytes, 0644); err != nil {
		t.Fatalf("failed to write last assistant image: %v", err)
	}

	ctx := WithToolWorkspace(context.Background(), tmpDir)
	ctx = WithLastAssistantImage(ctx, &providers.MediaRef{
		ID:       "last",
		Kind:     "image",
		MimeType: "image/png",
		Path:     lastAssistantFile,
	})

	refs, err := NewCreateImageTool(providers.NewRegistry(nil)).resolveReferenceImages(ctx, map[string]any{
		"use_current_images": false,
	})
	if err != nil {
		t.Fatalf("resolveReferenceImages returned error: %v", err)
	}
	if len(refs) != 0 {
		t.Fatalf("expected opt-out to keep references empty, got %d", len(refs))
	}
}

// TestCreateImageTool_NoHistoryNoAutoAttach verifies the no-history fallback:
// when no last assistant image is in context, the tool runs without any
// reference. This is the existing text-to-image behavior; the new branch
// must not change it.
func TestCreateImageTool_NoHistoryNoAutoAttach(t *testing.T) {
	tmpDir := t.TempDir()

	// No last assistant image, no current uploads.
	ctx := WithToolWorkspace(context.Background(), tmpDir)

	refs, err := NewCreateImageTool(providers.NewRegistry(nil)).resolveReferenceImages(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("resolveReferenceImages returned error: %v", err)
	}
	if len(refs) != 0 {
		t.Fatalf("expected no references when no history and no current uploads, got %d", len(refs))
	}
}

// TestCreateImageTool_CurrentRunWinsOverLastAssistant verifies the priority
// order: if the user uploaded an image in the current turn AND a prior
// assistant image exists, the current-turn upload wins (existing behavior
// preserved). The last-assistant branch only fires when nothing else
// supplied a reference.
func TestCreateImageTool_CurrentRunWinsOverLastAssistant(t *testing.T) {
	tmpDir := t.TempDir()
	lastFile := filepath.Join(tmpDir, "last-assistant.png")
	pngBytes := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	if err := os.WriteFile(lastFile, pngBytes, 0644); err != nil {
		t.Fatalf("failed to write last assistant image: %v", err)
	}

	ctx := WithToolWorkspace(context.Background(), tmpDir)
	ctx = WithLastAssistantImage(ctx, &providers.MediaRef{
		ID:       "last",
		Kind:     "image",
		MimeType: "image/png",
		Path:     lastFile,
	})
	ctx = WithCurrentRunImages(ctx, []providers.ImageContent{{
		MimeType: "image/png",
		Data:     base64.StdEncoding.EncodeToString(pngBytes),
	}})

	refs, err := NewCreateImageTool(providers.NewRegistry(nil)).resolveReferenceImages(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("resolveReferenceImages returned error: %v", err)
	}
	// Exactly 1 ref expected — the current-turn upload. The last-assistant
	// branch should NOT have fired because len(results) was already 1 after
	// the current-turn auto-attach block.
	if len(refs) != 1 {
		t.Fatalf("expected 1 reference (current-turn wins), got %d", len(refs))
	}
	if refs[0].Description != "" {
		// Current-turn images are resolved with an empty description; the
		// last-assistant branch would set it to "last assistant image".
		t.Errorf("ref[0].Description = %q, want empty (current-turn upload)",
			refs[0].Description)
	}
}
