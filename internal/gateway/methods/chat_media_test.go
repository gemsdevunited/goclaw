package methods

import (
	"strings"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/agent"
	httpapi "github.com/nextlevelbuilder/goclaw/internal/http"
)

func TestSignChatMediaResultsReturnsSignedFileURLsWithoutMutatingRunResult(t *testing.T) {
	const secret = "test-file-signing-secret"
	media := []agent.MediaResult{{
		Path:        "/app/workspace/gemie/ws/dev2_gemsunited_com/generated/dog-studying.png",
		ContentType: "image/png",
	}}

	signed := signChatMediaResults(media, secret)

	if media[0].Path != "/app/workspace/gemie/ws/dev2_gemsunited_com/generated/dog-studying.png" {
		t.Fatalf("original media result was mutated: %q", media[0].Path)
	}
	if !strings.HasPrefix(signed[0].Path, "/v1/files/app/workspace/gemie/ws/dev2_gemsunited_com/generated/dog-studying.png?ft=") {
		t.Fatalf("expected signed file URL, got %q", signed[0].Path)
	}

	path := strings.SplitN(signed[0].Path, "?", 2)[0]
	token := strings.TrimPrefix(signed[0].Path, path+"?ft=")
	if !httpapi.VerifyFileToken(token, path, secret) {
		t.Fatal("expected response media token to be valid")
	}
}
