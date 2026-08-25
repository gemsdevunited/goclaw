package tools

import (
	"context"

	"github.com/nextlevelbuilder/goclaw/internal/providers"
)

// lastAssistantImageKey is the unexported context key for the most recent
// assistant image MediaRef. Used by the create_image tool to auto-attach a
// reference when the LLM omits ref_images on a refinement turn.
type lastAssistantImageKey struct{}

// WithLastAssistantImage stores a pointer to the most recent assistant image
// MediaRef in the context so downstream tools (specifically create_image) can
// use it as a default reference when the LLM did not pass ref_images explicitly.
// A nil ref is a no-op (the returned context equals the input).
func WithLastAssistantImage(ctx context.Context, ref *providers.MediaRef) context.Context {
	if ref == nil {
		return ctx
	}
	return context.WithValue(ctx, lastAssistantImageKey{}, ref)
}

// LastAssistantImageFromCtx returns the most recent assistant image stored in
// context, or nil if none. The returned pointer is read-only; do not mutate
// its fields, and do not assume it is set across all tool invocations.
func LastAssistantImageFromCtx(ctx context.Context) *providers.MediaRef {
	if v, ok := ctx.Value(lastAssistantImageKey{}).(*providers.MediaRef); ok {
		return v
	}
	return nil
}
