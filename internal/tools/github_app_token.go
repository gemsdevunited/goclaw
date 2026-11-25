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
	// ClientID (e.g. "Iv23li…") is GitHub's recommended JWT issuer. AppID is
	// the legacy numeric issuer, still accepted. At least one is required;
	// ClientID wins when both are set.
	ClientID string
	AppID    string
	// InstallationID is optional: when empty it is auto-resolved with the App
	// JWT (via Owner, or the App's single installation) and cached.
	InstallationID string
	// Owner is the org/user login the App is installed on; used only to
	// resolve InstallationID when it is not given.
	Owner         string
	PrivateKeyPEM string
	// Repositories optionally downscopes the token to these repo names.
	Repositories []string
	// Permissions optionally downscopes the token (e.g. contents=write).
	Permissions map[string]string
}

var (
	errGitHubAppBlobEmpty       = errors.New("github app: credential blob is empty")
	errGitHubAppIssuerMissing   = errors.New("github app: client_id (recommended) or app_id is required")
	errGitHubAppClientIDInvalid = errors.New("github app: client_id has an invalid format")
	errGitHubAppIDInvalid       = errors.New("github app: app_id must be a positive integer")
	errGitHubAppInstallInvalid  = errors.New("github app: installation_id must be a positive integer")
	errGitHubAppOwnerInvalid    = errors.New("github app: owner must be a valid GitHub org or user login")
	githubAppClientIDRe         = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	githubLoginRe               = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
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
//	{"client_id":"Iv23li...","private_key":"-----BEGIN ...",
//	 "app_id":"123","installation_id":"456","owner":"my-org",          (all optional)
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
		ClientID:       strings.TrimSpace(m["client_id"]),
		AppID:          strings.TrimSpace(m["app_id"]),
		InstallationID: strings.TrimSpace(m["installation_id"]),
		Owner:          strings.TrimSpace(m["owner"]),
		PrivateKeyPEM:  strings.ReplaceAll(strings.TrimSpace(m["private_key"]), "\r\n", "\n"),
	}
	if c.ClientID == "" && c.AppID == "" {
		return nil, errGitHubAppIssuerMissing
	}
	if c.ClientID != "" && !githubAppClientIDRe.MatchString(c.ClientID) {
		return nil, errGitHubAppClientIDInvalid
	}
	if c.AppID != "" && !isPositiveInt(c.AppID) {
		return nil, errGitHubAppIDInvalid
	}
	if c.InstallationID != "" && !isPositiveInt(c.InstallationID) {
		return nil, errGitHubAppInstallInvalid
	}
	if c.Owner != "" && !githubLoginRe.MatchString(c.Owner) {
		return nil, errGitHubAppOwnerInvalid
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
	m := map[string]string{"private_key": c.PrivateKeyPEM}
	for k, v := range map[string]string{
		"client_id":       c.ClientID,
		"app_id":          c.AppID,
		"installation_id": c.InstallationID,
		"owner":           c.Owner,
	} {
		if v != "" {
			m[k] = v
		}
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

// Issuer returns the JWT `iss` claim: client ID (GitHub's recommendation)
// when set, otherwise the legacy numeric App ID.
func (c *GitHubAppCredential) Issuer() string {
	if c.ClientID != "" {
		return c.ClientID
	}
	return c.AppID
}

// githubAppJWT builds the App JWT GitHub expects: RS256, iss=client id (or
// app id), iat backdated 60s for clock drift, exp 9 minutes out (max is 10).
func githubAppJWT(issuer string, key *rsa.PrivateKey, now time.Time) (string, error) {
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]any{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": issuer,
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
	githubAppInstallCache sync.Map // cacheKey → resolved installation id (string)
	githubAppTokenFlights singleflight.Group
)

func githubAppCacheKey(host string, c *GitHubAppCredential) string {
	h := sha256.New()
	fields := c.NormalizedFields()
	for _, k := range []string{"client_id", "app_id", "installation_id", "owner", "private_key", "repositories", "permissions"} {
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
		tok, err := requestGitHubAppToken(context.WithoutCancel(ctx), host, key, c)
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

// githubAppAPIError is a non-2xx GitHub API response. Message is GitHub's
// `message` field (never contains credential material).
type githubAppAPIError struct {
	op      string
	status  int
	message string
}

func (e *githubAppAPIError) Error() string {
	return fmt.Sprintf("github app: %s failed (HTTP %d): %s", e.op, e.status, e.message)
}

func isGitHubAppNotFound(err error) bool {
	var apiErr *githubAppAPIError
	return errors.As(err, &apiErr) && apiErr.status == http.StatusNotFound
}

// githubAppDo performs one App-JWT-authenticated API call and decodes a 2xx
// JSON body into out.
func githubAppDo(ctx context.Context, op, method, url, jwt string, body []byte, out any) error {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return fmt.Errorf("github app: build %s request: %w", op, err)
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := githubAppHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("github app: %s: %w", op, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var apiErr struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(respBody, &apiErr)
		msg := apiErr.Message
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		return &githubAppAPIError{op: op, status: resp.StatusCode, message: msg}
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("github app: decode %s response: %w", op, err)
	}
	return nil
}

type githubAppInstallation struct {
	ID      int64 `json:"id"`
	Account struct {
		Login string `json:"login"`
	} `json:"account"`
}

// resolveGitHubAppInstallation finds the installation id the way hosted
// GitHub Apps do — with the App JWT, no org-settings access required:
//   - owner set → GET /orgs/{owner}/installation, falling back to
//     /users/{owner}/installation for personal accounts;
//   - otherwise → GET /app/installations, which must contain exactly one.
func resolveGitHubAppInstallation(ctx context.Context, base, jwt string, c *GitHubAppCredential) (string, error) {
	var inst githubAppInstallation
	if c.Owner != "" {
		err := githubAppDo(ctx, "resolve installation for "+c.Owner, http.MethodGet, base+"/orgs/"+c.Owner+"/installation", jwt, nil, &inst)
		if isGitHubAppNotFound(err) {
			err = githubAppDo(ctx, "resolve installation for "+c.Owner, http.MethodGet, base+"/users/"+c.Owner+"/installation", jwt, nil, &inst)
		}
		if isGitHubAppNotFound(err) {
			return "", fmt.Errorf("github app: the App is not installed on %q — install it on that org/user first", c.Owner)
		}
		if err != nil {
			return "", err
		}
		return strconv.FormatInt(inst.ID, 10), nil
	}
	var list []githubAppInstallation
	if err := githubAppDo(ctx, "list installations", http.MethodGet, base+"/app/installations?per_page=100", jwt, nil, &list); err != nil {
		return "", err
	}
	switch len(list) {
	case 0:
		return "", errors.New("github app: the App has no installations — install it on an org or user first")
	case 1:
		return strconv.FormatInt(list[0].ID, 10), nil
	}
	owners := make([]string, 0, len(list))
	for _, it := range list {
		owners = append(owners, it.Account.Login)
	}
	sort.Strings(owners)
	return "", fmt.Errorf("github app: the App is installed on multiple accounts (%s) — set owner to pick one", strings.Join(owners, ", "))
}

// requestGitHubAppToken mints one installation access token. When the
// installation id was auto-resolved and GitHub answers 404 (App uninstalled
// and re-installed → new id), the cached id is dropped and resolution retried once.
func requestGitHubAppToken(ctx context.Context, host, cacheKey string, c *GitHubAppCredential) (githubAppCachedToken, error) {
	key, err := parseGitHubAppPrivateKey(c.PrivateKeyPEM)
	if err != nil {
		return githubAppCachedToken{}, err
	}
	jwt, err := githubAppJWT(c.Issuer(), key, githubAppNow())
	if err != nil {
		return githubAppCachedToken{}, err
	}
	base := githubAPIBase(host)

	body := map[string]any{}
	if len(c.Repositories) > 0 {
		body["repositories"] = c.Repositories
	}
	if len(c.Permissions) > 0 {
		body["permissions"] = c.Permissions
	}
	payload, _ := json.Marshal(body)

	autoResolved := c.InstallationID == ""
	for attempt := 0; ; attempt++ {
		installationID := c.InstallationID
		if autoResolved {
			if v, ok := githubAppInstallCache.Load(cacheKey); ok {
				installationID = v.(string)
			} else {
				id, err := resolveGitHubAppInstallation(ctx, base, jwt, c)
				if err != nil {
					return githubAppCachedToken{}, err
				}
				githubAppInstallCache.Store(cacheKey, id)
				installationID = id
			}
		}

		var out struct {
			Token     string    `json:"token"`
			ExpiresAt time.Time `json:"expires_at"`
		}
		err := githubAppDo(ctx, "installation token request", http.MethodPost,
			base+"/app/installations/"+installationID+"/access_tokens", jwt, payload, &out)
		if autoResolved && attempt == 0 && isGitHubAppNotFound(err) {
			githubAppInstallCache.Delete(cacheKey)
			continue
		}
		if err != nil {
			return githubAppCachedToken{}, err
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
}
