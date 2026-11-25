package tools

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// GitHub App credential support.
//
// A `github_app` typed credential stores the App's private key; the agent
// never sees it. At exec time the gh/git adapters call MintGitHubAppToken,
// which signs a short-lived RS256 JWT, exchanges it for an installation access
// token (1h TTL, optionally downscoped to repositories/permissions), and caches
// the token process-wide until it is close to expiry.

// GitHubAppCredential is the decoded `github_app` credential blob.
type GitHubAppCredential struct {
	AppID          string
	InstallationID string
	PrivateKeyPEM  string
	// Repositories optionally downscopes the token to these repo names.
	Repositories []string
	// Permissions optionally downscopes the token (e.g. contents=write).
	Permissions map[string]string
}

var (
	errGitHubAppBlobEmpty       = errors.New("github app: credential blob is empty")
	errGitHubAppIDInvalid       = errors.New("github app: app_id must be a positive integer")
	errGitHubAppInstallInvalid  = errors.New("github app: installation_id must be a positive integer")
	errGitHubAppKeyMissing      = errors.New("github app: private_key is empty")
	errGitHubAppKeyInvalid      = errors.New("github app: private_key is not a valid RSA private key PEM")
	errGitHubAppRepoInvalid     = errors.New("github app: repositories contains an invalid repository name")
	errGitHubAppPermInvalid     = errors.New("github app: permissions must be a comma list of name:read|write|admin")
	errGitHubAppTokenMissing    = errors.New("github app: token response has no token")
	githubAppRepoNameRe         = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
	githubAppPermNameRe         = regexp.MustCompile(`^[a-z_]{1,64}$`)
	githubAppValidPermLevels    = map[string]bool{"read": true, "write": true, "admin": true}
	githubAppTokenRefreshMargin = 5 * time.Minute
)

// ParseGitHubAppBlob decodes and validates the stored credential blob. The wire
// shape is a flat string map so it round-trips through the same typed-credential
// storage as pat/ssh_key:
//
//	{"app_id":"123","installation_id":"456","private_key":"-----BEGIN ...",
//	 "repositories":"repo-a,repo-b","permissions":"contents:write,pull_requests:write"}
func ParseGitHubAppBlob(blob []byte) (*GitHubAppCredential, error) {
	if len(blob) == 0 {
		return nil, errGitHubAppBlobEmpty
	}
	var m map[string]string
	if err := json.Unmarshal(blob, &m); err != nil {
		return nil, fmt.Errorf("github app: decode blob: %w", err)
	}
	return ParseGitHubAppFields(m)
}

// ParseGitHubAppFields validates the github_app fields from a flat map. Shared
// by the HTTP save path (offline validation) and the runtime adapters.
func ParseGitHubAppFields(m map[string]string) (*GitHubAppCredential, error) {
	c := &GitHubAppCredential{
		AppID:          strings.TrimSpace(m["app_id"]),
		InstallationID: strings.TrimSpace(m["installation_id"]),
		PrivateKeyPEM:  strings.ReplaceAll(strings.TrimSpace(m["private_key"]), "\r\n", "\n"),
	}
	if !isPositiveInt(c.AppID) {
		return nil, errGitHubAppIDInvalid
	}
	if !isPositiveInt(c.InstallationID) {
		return nil, errGitHubAppInstallInvalid
	}
	if c.PrivateKeyPEM == "" {
		return nil, errGitHubAppKeyMissing
	}
	if _, err := parseGitHubAppPrivateKey(c.PrivateKeyPEM); err != nil {
		return nil, err
	}
	for _, r := range splitCommaList(m["repositories"]) {
		if !githubAppRepoNameRe.MatchString(r) {
			return nil, errGitHubAppRepoInvalid
		}
		c.Repositories = append(c.Repositories, r)
	}
	for _, p := range splitCommaList(m["permissions"]) {
		name, level, ok := strings.Cut(p, ":")
		name, level = strings.TrimSpace(name), strings.TrimSpace(level)
		if !ok || !githubAppPermNameRe.MatchString(name) || !githubAppValidPermLevels[level] {
			return nil, errGitHubAppPermInvalid
		}
		if c.Permissions == nil {
			c.Permissions = map[string]string{}
		}
		c.Permissions[name] = level
	}
	return c, nil
}

// NormalizedFields returns the canonical blob map written to storage.
func (c *GitHubAppCredential) NormalizedFields() map[string]string {
	m := map[string]string{
		"app_id":          c.AppID,
		"installation_id": c.InstallationID,
		"private_key":     c.PrivateKeyPEM,
	}
	if len(c.Repositories) > 0 {
		m["repositories"] = strings.Join(c.Repositories, ",")
	}
	if len(c.Permissions) > 0 {
		keys := make([]string, 0, len(c.Permissions))
		for k := range c.Permissions {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, k+":"+c.Permissions[k])
		}
		m["permissions"] = strings.Join(parts, ",")
	}
	return m
}

func isPositiveInt(s string) bool {
	n, err := strconv.ParseInt(s, 10, 64)
	return err == nil && n > 0
}

func splitCommaList(s string) []string {
	var out []string
	for part := range strings.SplitSeq(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func parseGitHubAppPrivateKey(pemStr string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errGitHubAppKeyInvalid
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errGitHubAppKeyInvalid
	}
	rsaKey, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errGitHubAppKeyInvalid
	}
	return rsaKey, nil
}

// githubAppJWT builds the App JWT GitHub expects: RS256, iss=app id,
// iat backdated 60s for clock drift, exp 9 minutes out (max allowed is 10).
func githubAppJWT(appID string, key *rsa.PrivateKey, now time.Time) (string, error) {
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]any{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": appID,
	})
	if err != nil {
		return "", err
	}
	signingInput := header + "." + enc.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("github app: sign jwt: %w", err)
	}
	return signingInput + "." + enc.EncodeToString(sig), nil
}

// githubAPIBase maps a credential host_scope to its REST API root.
func githubAPIBase(host string) string {
	if githubAppAPIBaseOverride != "" {
		return githubAppAPIBaseOverride
	}
	if host == "" || host == "github.com" {
		return "https://api.github.com"
	}
	return "https://" + host + "/api/v3"
}

// Test seams.
var (
	githubAppAPIBaseOverride string
	githubAppHTTPClient      = &http.Client{Timeout: 15 * time.Second}
	githubAppNow             = time.Now
)

type githubAppCachedToken struct {
	token     string
	expiresAt time.Time
}

var (
	githubAppTokenCache   sync.Map // cacheKey → githubAppCachedToken
	githubAppTokenFlights singleflight.Group
)

func githubAppCacheKey(host string, c *GitHubAppCredential) string {
	h := sha256.New()
	fields := c.NormalizedFields()
	for _, k := range []string{"app_id", "installation_id", "private_key", "repositories", "permissions"} {
		h.Write([]byte(k + "=" + fields[k] + "\x00"))
	}
	h.Write([]byte("host=" + host))
	return hex.EncodeToString(h.Sum(nil))
}

// MintGitHubAppToken returns a valid installation access token for the
// credential, minting a new one when the cached token is missing or within the
// refresh margin of expiry. Concurrent callers for the same credential share a
// single in-flight request.
func MintGitHubAppToken(ctx context.Context, host string, c *GitHubAppCredential) (string, error) {
	key := githubAppCacheKey(host, c)
	if v, ok := githubAppTokenCache.Load(key); ok {
		cached := v.(githubAppCachedToken)
		if githubAppNow().Add(githubAppTokenRefreshMargin).Before(cached.expiresAt) {
			return cached.token, nil
		}
	}
	v, err, _ := githubAppTokenFlights.Do(key, func() (any, error) {
		// Re-check after winning the flight — another caller may have refreshed.
		if v, ok := githubAppTokenCache.Load(key); ok {
			cached := v.(githubAppCachedToken)
			if githubAppNow().Add(githubAppTokenRefreshMargin).Before(cached.expiresAt) {
				return cached.token, nil
			}
		}
		// Detach from the caller's cancellation so one cancelled exec doesn't
		// fail every caller sharing this flight; the HTTP client timeout bounds it.
		tok, err := requestGitHubAppToken(context.WithoutCancel(ctx), host, c)
		if err != nil {
			return "", err
		}
		githubAppTokenCache.Store(key, tok)
		return tok.token, nil
	})
	if err != nil {
		return "", err
	}
	return v.(string), nil
}

func requestGitHubAppToken(ctx context.Context, host string, c *GitHubAppCredential) (githubAppCachedToken, error) {
	key, err := parseGitHubAppPrivateKey(c.PrivateKeyPEM)
	if err != nil {
		return githubAppCachedToken{}, err
	}
	jwt, err := githubAppJWT(c.AppID, key, githubAppNow())
	if err != nil {
		return githubAppCachedToken{}, err
	}
	body := map[string]any{}
	if len(c.Repositories) > 0 {
		body["repositories"] = c.Repositories
	}
	if len(c.Permissions) > 0 {
		body["permissions"] = c.Permissions
	}
	payload, _ := json.Marshal(body)
	url := githubAPIBase(host) + "/app/installations/" + c.InstallationID + "/access_tokens"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return githubAppCachedToken{}, fmt.Errorf("github app: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/json")
	resp, err := githubAppHTTPClient.Do(req)
	if err != nil {
		return githubAppCachedToken{}, fmt.Errorf("github app: request installation token: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		var apiErr struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(respBody, &apiErr)
		msg := apiErr.Message
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		return githubAppCachedToken{}, fmt.Errorf("github app: installation token request failed (HTTP %d): %s", resp.StatusCode, msg)
	}
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil {
		return githubAppCachedToken{}, fmt.Errorf("github app: decode token response: %w", err)
	}
	if out.Token == "" {
		return githubAppCachedToken{}, errGitHubAppTokenMissing
	}
	if err := validateTokenShape(out.Token); err != nil {
		return githubAppCachedToken{}, err
	}
	if out.ExpiresAt.IsZero() {
		out.ExpiresAt = githubAppNow().Add(55 * time.Minute)
	}
	return githubAppCachedToken{token: out.Token, expiresAt: out.ExpiresAt}, nil
}
