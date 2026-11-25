package tools

import (
	"context"
	"fmt"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// ghAdapter implements the CredentialAdapter contract for the GitHub CLI.
//
// Credential types:
//   - env / none   → no-op; the legacy GH_TOKEN env (binary/grant/user env)
//     is injected by the passthrough path exactly as before.
//   - pat          → GH_TOKEN from the typed blob.
//   - github_app   → installation token minted per exec (cached ~1h) from the
//     stored App private key; the key itself never reaches the child process.
//
// For GitHub Enterprise Server hosts the token is exposed as
// GH_ENTERPRISE_TOKEN with GH_HOST set, which is how gh selects a non-github.com
// host without a hosts.yml login.
type ghAdapter struct{}

func (ghAdapter) Name() string { return "gh" }

// ShouldInject is always true: almost every gh subcommand talks to the API.
func (ghAdapter) ShouldInject([]string) bool { return true }

// sandboxSafe: gh injection is env-only (no host tmpfiles), so it works when
// the exec is routed into a sandbox container.
func (ghAdapter) sandboxSafe() bool { return true }

func (ghAdapter) Prepare(ctx context.Context, _ *store.SecureCLIBinary, cred *store.SecureCLIUserCredential, _ []string) (*Injection, error) {
	if cred == nil || cred.CredentialType == nil {
		return &Injection{}, nil
	}
	typ := *cred.CredentialType
	if typ == "" || typ == "env" {
		return &Injection{}, nil
	}
	host := "github.com"
	if cred.HostScope != nil && *cred.HostScope != "" {
		h, err := normalizeHost(*cred.HostScope)
		if err != nil {
			return nil, fmt.Errorf("gh adapter: normalize credential host: %w", err)
		}
		host = h
	}

	var token string
	switch typ {
	case "pat":
		t, err := decodePATToken(cred.EncryptedEnv)
		if err != nil {
			return nil, err
		}
		token = t
	case "github_app":
		app, err := ParseGitHubAppBlob(cred.EncryptedEnv)
		if err != nil {
			return nil, err
		}
		t, err := MintGitHubAppToken(ctx, host, app)
		if err != nil {
			return nil, err
		}
		token = t
	default:
		return nil, fmt.Errorf("gh adapter: unsupported credential_type %q", typ)
	}
	if err := validateTokenShape(token); err != nil {
		return nil, err
	}

	env := map[string]string{
		"GH_PROMPT_DISABLED":    "1",
		"GH_NO_UPDATE_NOTIFIER": "1",
	}
	if host == "github.com" {
		env["GH_TOKEN"] = token
	} else {
		env["GH_HOST"] = host
		env["GH_ENTERPRISE_TOKEN"] = token
	}
	return &Injection{Env: env, ScrubValues: []string{token}}, nil
}

func init() {
	RegisterAdapter(ghAdapter{})
}
