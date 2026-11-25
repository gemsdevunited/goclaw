package tools

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

func testGitHubAppKey(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemStr := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	return key, pemStr
}

// fakeGitHubAppServer emulates the GitHub App endpoints GoClaw uses and
// verifies the App JWT signature + issuer.
type fakeGitHubAppServer struct {
	srv      *httptest.Server
	calls    atomic.Int32 // access_tokens calls
	lookups  atomic.Int32 // installation lookup calls
	lastBody atomic.Value // map[string]any
	lastIss  atomic.Value // string
	expires  time.Duration
	status   int
	// installations served by GET /app/installations and /orgs|users/{login}/installation.
	mu            sync.Mutex
	installations []githubAppInstallation
	liveIDs       map[string]bool // installation ids whose access_tokens succeed
}

func newFakeGitHubAppServer(t *testing.T, pub *rsa.PublicKey, issuer string) *fakeGitHubAppServer {
	t.Helper()
	f := &fakeGitHubAppServer{expires: time.Hour, status: http.StatusCreated, liveIDs: map[string]bool{"456": true}}
	inst := githubAppInstallation{ID: 456}
	inst.Account.Login = "gemsdevunited"
	f.installations = []githubAppInstallation{inst}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jwt := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		parts := strings.Split(jwt, ".")
		if len(parts) != 3 {
			http.Error(w, `{"message":"bad jwt"}`, http.StatusUnauthorized)
			return
		}
		sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
		digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig); err != nil {
			http.Error(w, `{"message":"A JSON web token could not be decoded"}`, http.StatusUnauthorized)
			return
		}
		claimsRaw, _ := base64.RawURLEncoding.DecodeString(parts[1])
		var claims map[string]any
		_ = json.Unmarshal(claimsRaw, &claims)
		f.lastIss.Store(claims["iss"])
		if claims["iss"] != issuer {
			http.Error(w, `{"message":"wrong iss"}`, http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		insts := append([]githubAppInstallation(nil), f.installations...)
		live := map[string]bool{}
		for k, v := range f.liveIDs {
			live[k] = v
		}
		f.mu.Unlock()

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/app/installations":
			f.lookups.Add(1)
			_ = json.NewEncoder(w).Encode(insts)
			return
		case r.Method == http.MethodGet && (strings.HasPrefix(r.URL.Path, "/orgs/") || strings.HasPrefix(r.URL.Path, "/users/")) && strings.HasSuffix(r.URL.Path, "/installation"):
			f.lookups.Add(1)
			login := strings.Split(r.URL.Path, "/")[2]
			for _, it := range insts {
				if it.Account.Login == login && strings.HasPrefix(r.URL.Path, "/users/") == (login == "solo-user") {
					_ = json.NewEncoder(w).Encode(it)
					return
				}
			}
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/app/installations/") && strings.HasSuffix(r.URL.Path, "/access_tokens"):
		default:
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}

		n := f.calls.Add(1)
		id := strings.Split(r.URL.Path, "/")[3]
		if !live[id] {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.lastBody.Store(body)
		if f.status != http.StatusCreated {
			w.WriteHeader(f.status)
			_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
			return
		}
		w.WriteHeader(http.StatusCreated)
		// 2026 stateless format: ghs_<APPID>_<JWT>, ~520 chars with dots.
		tok := "ghs_123_eyJhbGciOiJSUzI1NiJ9." + strings.Repeat("a", 400) + "." + strings.Repeat("b", 80) + string(rune('a'+n%26))
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      tok,
			"expires_at": githubAppNow().Add(f.expires).UTC().Format(time.RFC3339),
		})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func withGitHubAppTestServer(t *testing.T, f *fakeGitHubAppServer) {
	t.Helper()
	prevBase, prevClient := githubAppAPIBaseOverride, githubAppHTTPClient
	githubAppAPIBaseOverride = f.srv.URL
	githubAppHTTPClient = f.srv.Client()
	githubAppTokenCache = sync.Map{}
	githubAppInstallCache = sync.Map{}
	t.Cleanup(func() {
		githubAppAPIBaseOverride, githubAppHTTPClient = prevBase, prevClient
		githubAppTokenCache = sync.Map{}
		githubAppInstallCache = sync.Map{}
	})
}

func TestParseGitHubAppFields(t *testing.T) {
	_, pemStr := testGitHubAppKey(t)
	ok := map[string]string{"app_id": "123", "installation_id": "456", "private_key": pemStr}

	c, err := ParseGitHubAppFields(ok)
	if err != nil {
		t.Fatalf("valid fields rejected: %v", err)
	}
	if c.AppID != "123" || c.InstallationID != "456" {
		t.Fatalf("unexpected parse: %+v", c)
	}

	withScope := map[string]string{
		"app_id": "123", "installation_id": "456", "private_key": strings.ReplaceAll(pemStr, "\n", "\r\n"),
		"repositories": " repo-a , repo.b ", "permissions": "pull_requests:write, contents:read",
	}
	c, err = ParseGitHubAppFields(withScope)
	if err != nil {
		t.Fatalf("scoped fields rejected: %v", err)
	}
	got := c.NormalizedFields()
	if got["repositories"] != "repo-a,repo.b" || got["permissions"] != "contents:read,pull_requests:write" {
		t.Fatalf("normalization wrong: %+v", got)
	}
	if strings.Contains(got["private_key"], "\r") {
		t.Fatal("CRLF not normalized in private key")
	}

	bad := []struct {
		name string
		mut  func(m map[string]string)
		want error
	}{
		{"app id non-numeric", func(m map[string]string) { m["app_id"] = "abc" }, errGitHubAppIDInvalid},
		{"app id zero", func(m map[string]string) { m["app_id"] = "0" }, errGitHubAppIDInvalid},
		{"installation non-numeric", func(m map[string]string) { m["installation_id"] = "abc" }, errGitHubAppInstallInvalid},
		{"no issuer", func(m map[string]string) { delete(m, "app_id") }, errGitHubAppIssuerMissing},
		{"client id invalid", func(m map[string]string) { m["client_id"] = "Iv23 li" }, errGitHubAppClientIDInvalid},
		{"owner invalid", func(m map[string]string) { m["owner"] = "-bad/owner" }, errGitHubAppOwnerInvalid},
		{"key missing", func(m map[string]string) { m["private_key"] = "" }, errGitHubAppKeyMissing},
		{"key garbage", func(m map[string]string) { m["private_key"] = "not a key" }, errGitHubAppKeyInvalid},
		{"repo invalid", func(m map[string]string) { m["repositories"] = "owner/repo" }, errGitHubAppRepoInvalid},
		{"perm level invalid", func(m map[string]string) { m["permissions"] = "contents:all" }, errGitHubAppPermInvalid},
		{"perm missing level", func(m map[string]string) { m["permissions"] = "contents" }, errGitHubAppPermInvalid},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			m := map[string]string{}
			for k, v := range ok {
				m[k] = v
			}
			tc.mut(m)
			if _, err := ParseGitHubAppFields(m); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestParseGitHubAppFields_PKCS8(t *testing.T) {
	key, _ := testGitHubAppKey(t)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pemStr := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	if _, err := ParseGitHubAppFields(map[string]string{"app_id": "1", "installation_id": "2", "private_key": pemStr}); err != nil {
		t.Fatalf("PKCS8 key rejected: %v", err)
	}
}

func TestMintGitHubAppToken_CachesAndDownscopes(t *testing.T) {
	key, pemStr := testGitHubAppKey(t)
	f := newFakeGitHubAppServer(t, &key.PublicKey, "123")
	withGitHubAppTestServer(t, f)

	c, err := ParseGitHubAppFields(map[string]string{
		"app_id": "123", "installation_id": "456", "private_key": pemStr,
		"repositories": "GemSFastify", "permissions": "contents:write",
	})
	if err != nil {
		t.Fatal(err)
	}
	tok1, err := MintGitHubAppToken(context.Background(), "github.com", c)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	tok2, err := MintGitHubAppToken(context.Background(), "github.com", c)
	if err != nil {
		t.Fatalf("mint (cached): %v", err)
	}
	if tok1 != tok2 || f.calls.Load() != 1 {
		t.Fatalf("expected cached token reuse: tok1=%q tok2=%q calls=%d", tok1, tok2, f.calls.Load())
	}
	body, _ := f.lastBody.Load().(map[string]any)
	repos, _ := body["repositories"].([]any)
	perms, _ := body["permissions"].(map[string]any)
	if len(repos) != 1 || repos[0] != "GemSFastify" || perms["contents"] != "write" {
		t.Fatalf("downscope not sent: %+v", body)
	}
}

func TestMintGitHubAppToken_RefreshesNearExpiry(t *testing.T) {
	key, pemStr := testGitHubAppKey(t)
	f := newFakeGitHubAppServer(t, &key.PublicKey, "123")
	f.expires = 2 * time.Minute // inside the 5-minute refresh margin
	withGitHubAppTestServer(t, f)

	c, _ := ParseGitHubAppFields(map[string]string{"app_id": "123", "installation_id": "456", "private_key": pemStr})
	for range 2 {
		if _, err := MintGitHubAppToken(context.Background(), "github.com", c); err != nil {
			t.Fatal(err)
		}
	}
	if f.calls.Load() != 2 {
		t.Fatalf("expected refresh on each call near expiry, calls=%d", f.calls.Load())
	}
}

func TestMintGitHubAppToken_ConcurrentSingleFlight(t *testing.T) {
	key, pemStr := testGitHubAppKey(t)
	f := newFakeGitHubAppServer(t, &key.PublicKey, "123")
	withGitHubAppTestServer(t, f)

	c, _ := ParseGitHubAppFields(map[string]string{"app_id": "123", "installation_id": "456", "private_key": pemStr})
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			if _, err := MintGitHubAppToken(context.Background(), "github.com", c); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if f.calls.Load() != 1 {
		t.Fatalf("expected one upstream call, got %d", f.calls.Load())
	}
}

func TestMintGitHubAppToken_APIErrorSurfaced(t *testing.T) {
	key, pemStr := testGitHubAppKey(t)
	f := newFakeGitHubAppServer(t, &key.PublicKey, "123")
	f.status = http.StatusUnauthorized
	withGitHubAppTestServer(t, f)

	c, _ := ParseGitHubAppFields(map[string]string{"app_id": "123", "installation_id": "456", "private_key": pemStr})
	_, err := MintGitHubAppToken(context.Background(), "github.com", c)
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") || !strings.Contains(err.Error(), "Bad credentials") {
		t.Fatalf("expected surfaced API error, got %v", err)
	}
	if strings.Contains(err.Error(), "PRIVATE KEY") {
		t.Fatal("error leaked key material")
	}
}

func TestGitHubAPIBase(t *testing.T) {
	if got := githubAPIBase("github.com"); got != "https://api.github.com" {
		t.Fatalf("github.com base = %q", got)
	}
	if got := githubAPIBase("ghe.example.com"); got != "https://ghe.example.com/api/v3" {
		t.Fatalf("GHES base = %q", got)
	}
}

func githubAppTypedCred(t *testing.T, pemStr, host string) *store.SecureCLIUserCredential {
	t.Helper()
	blob, _ := json.Marshal(map[string]string{"app_id": "123", "installation_id": "456", "private_key": pemStr})
	ct := "github_app"
	return &store.SecureCLIUserCredential{EncryptedEnv: blob, CredentialType: &ct, HostScope: &host}
}

func TestGHAdapter_GitHubApp(t *testing.T) {
	key, pemStr := testGitHubAppKey(t)
	f := newFakeGitHubAppServer(t, &key.PublicKey, "123")
	withGitHubAppTestServer(t, f)

	inj, err := ghAdapter{}.Prepare(context.Background(), nil, githubAppTypedCred(t, pemStr, "github.com"), []string{"pr", "list"})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	tok := inj.Env["GH_TOKEN"]
	if !strings.HasPrefix(tok, "ghs_123_") {
		t.Fatalf("GH_TOKEN not injected: %+v", inj.Env)
	}
	if len(inj.ScrubValues) != 1 || inj.ScrubValues[0] != tok {
		t.Fatalf("token not registered for scrubbing: %+v", inj.ScrubValues)
	}
	for k, v := range inj.Env {
		if strings.Contains(v, "PRIVATE KEY") {
			t.Fatalf("private key leaked into env %s", k)
		}
	}
}

func TestGHAdapter_EnterpriseHost(t *testing.T) {
	ct, host := "pat", "ghe.example.com"
	blob, _ := json.Marshal(map[string]string{"token": "ghp_enterprise"})
	inj, err := ghAdapter{}.Prepare(context.Background(), nil,
		&store.SecureCLIUserCredential{EncryptedEnv: blob, CredentialType: &ct, HostScope: &host}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if inj.Env["GH_HOST"] != host || inj.Env["GH_ENTERPRISE_TOKEN"] != "ghp_enterprise" || inj.Env["GH_TOKEN"] != "" {
		t.Fatalf("unexpected GHES env: %+v", inj.Env)
	}
}

func TestGHAdapter_LegacyEnvIsNoop(t *testing.T) {
	for _, cred := range []*store.SecureCLIUserCredential{nil, {}, {CredentialType: new("env")}} {
		inj, err := ghAdapter{}.Prepare(context.Background(), nil, cred, []string{"api", "user"})
		if err != nil {
			t.Fatal(err)
		}
		if len(inj.Env) != 0 || len(inj.ScrubValues) != 0 {
			t.Fatalf("legacy env cred must be a no-op, got %+v", inj)
		}
	}
	if !isSandboxSafeAdapter(ghAdapter{}) {
		t.Fatal("gh adapter must be sandbox-safe (env-only)")
	}
	if isSandboxSafeAdapter(gitAdapter{}) {
		t.Fatal("git adapter must not be sandbox-safe (ssh tmpfiles)")
	}
}

func TestGitAdapter_GitHubAppClone(t *testing.T) {
	key, pemStr := testGitHubAppKey(t)
	f := newFakeGitHubAppServer(t, &key.PublicKey, "123")
	withGitHubAppTestServer(t, f)

	inj, err := gitAdapter{}.Prepare(context.Background(), nil, githubAppTypedCred(t, pemStr, "github.com"),
		[]string{"clone", "https://github.com/org/repo.git"})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if inj.Env["GIT_CONFIG_KEY_0"] != "http.https://github.com/.extraheader" {
		t.Fatalf("unexpected config key: %+v", inj.Env)
	}
	if !strings.HasPrefix(inj.Env["GIT_CONFIG_VALUE_0"], "Authorization: Basic ") {
		t.Fatalf("missing auth header: %+v", inj.Env)
	}
	if len(inj.ScrubValues) != 3 {
		t.Fatalf("expected token, basic payload and header scrubbed: %+v", inj.ScrubValues)
	}

	// Host mismatch must fail before any mint.
	before := f.calls.Load()
	_, err = gitAdapter{}.Prepare(context.Background(), nil, githubAppTypedCred(t, pemStr, "github.com"),
		[]string{"clone", "https://gitlab.com/org/repo.git"})
	var mismatch *errCredentialHostMismatch
	if !errors.As(err, &mismatch) {
		t.Fatalf("expected host mismatch, got %v", err)
	}
	if f.calls.Load() != before {
		t.Fatal("token minted for mismatched host")
	}
}

func TestHasTypedEffectiveCredential(t *testing.T) {
	gh, app, env := "gh", "github_app", "env"
	cases := []struct {
		name string
		bin  *store.SecureCLIBinary
		want bool
	}{
		{"nil", nil, false},
		{"no adapter", &store.SecureCLIBinary{CredentialType: &app}, false},
		{"adapter env cred", &store.SecureCLIBinary{AdapterName: &gh, CredentialType: &env}, false},
		{"adapter agent typed", &store.SecureCLIBinary{AdapterName: &gh, CredentialType: &app}, true},
		{"adapter user typed", &store.SecureCLIBinary{AdapterName: &gh, UserCredentialType: &app}, true},
	}
	for _, tc := range cases {
		if got := hasTypedEffectiveCredential(tc.bin); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestGHPresetDeniesCodeExecSubcommands(t *testing.T) {
	deny, _ := json.Marshal(CLIPresets["gh"].DenyArgs)
	for _, args := range [][]string{
		{"extension", "install", "evil/gh-x"},
		{"ext", "exec", "x"},
		{"alias", "set", "x", "!sh"},
		{"config", "set", "editor", "vim"},
	} {
		if matchesBinaryDeny(args, deny) == "" {
			t.Errorf("expected %v to be denied", args)
		}
	}
	for _, args := range [][]string{
		{"pr", "list", "--json", "number"},
		{"issue", "comment", "1", "--body", "config set via extension docs"},
		{"config", "get", "git_protocol"},
	} {
		if p := matchesBinaryDeny(args, deny); p != "" {
			t.Errorf("expected %v to be allowed, denied by %q", args, p)
		}
	}
}

func TestParseGitHubAppFields_ClientIDOnly(t *testing.T) {
	_, pemStr := testGitHubAppKey(t)
	c, err := ParseGitHubAppFields(map[string]string{"client_id": "Iv23liAbC123", "private_key": pemStr, "owner": "gemsdevunited"})
	if err != nil {
		t.Fatalf("client-id-only credential rejected: %v", err)
	}
	if c.Issuer() != "Iv23liAbC123" {
		t.Fatalf("issuer = %q", c.Issuer())
	}
	both, _ := ParseGitHubAppFields(map[string]string{"client_id": "Iv23liAbC123", "app_id": "123", "private_key": pemStr})
	if both.Issuer() != "Iv23liAbC123" {
		t.Fatal("client_id must win over app_id as JWT issuer")
	}
	f := c.NormalizedFields()
	if _, ok := f["installation_id"]; ok {
		t.Fatal("empty optional fields must not be stored")
	}
}

func mintWith(t *testing.T, fields map[string]string) (string, error) {
	t.Helper()
	c, err := ParseGitHubAppFields(fields)
	if err != nil {
		t.Fatal(err)
	}
	return MintGitHubAppToken(context.Background(), "github.com", c)
}

func TestMintGitHubAppToken_ClientIDIssuerAutoResolvesSingleInstallation(t *testing.T) {
	key, pemStr := testGitHubAppKey(t)
	f := newFakeGitHubAppServer(t, &key.PublicKey, "Iv23liAbC123")
	withGitHubAppTestServer(t, f)

	tok, err := mintWith(t, map[string]string{"client_id": "Iv23liAbC123", "private_key": pemStr})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if len(tok) < 500 || !strings.HasPrefix(tok, "ghs_") {
		t.Fatalf("stateless-format token not accepted intact: len=%d", len(tok))
	}
	if f.lastIss.Load() != "Iv23liAbC123" {
		t.Fatalf("JWT iss = %v, want client id", f.lastIss.Load())
	}
	if f.lookups.Load() != 1 {
		t.Fatalf("expected one installation lookup, got %d", f.lookups.Load())
	}
}

func TestMintGitHubAppToken_OwnerLookupAndUserFallback(t *testing.T) {
	key, pemStr := testGitHubAppKey(t)
	f := newFakeGitHubAppServer(t, &key.PublicKey, "Iv1")
	solo := githubAppInstallation{ID: 789}
	solo.Account.Login = "solo-user"
	f.installations = append(f.installations, solo)
	f.liveIDs["789"] = true
	withGitHubAppTestServer(t, f)

	if _, err := mintWith(t, map[string]string{"client_id": "Iv1", "private_key": pemStr, "owner": "gemsdevunited"}); err != nil {
		t.Fatalf("org owner lookup: %v", err)
	}
	if _, err := mintWith(t, map[string]string{"client_id": "Iv1", "private_key": pemStr, "owner": "solo-user"}); err != nil {
		t.Fatalf("user fallback lookup: %v", err)
	}
	_, err := mintWith(t, map[string]string{"client_id": "Iv1", "private_key": pemStr, "owner": "not-installed"})
	if err == nil || !strings.Contains(err.Error(), "not installed on \"not-installed\"") {
		t.Fatalf("expected not-installed error, got %v", err)
	}
}

func TestMintGitHubAppToken_MultipleInstallationsNeedOwner(t *testing.T) {
	key, pemStr := testGitHubAppKey(t)
	f := newFakeGitHubAppServer(t, &key.PublicKey, "Iv1")
	other := githubAppInstallation{ID: 999}
	other.Account.Login = "another-org"
	f.installations = append(f.installations, other)
	withGitHubAppTestServer(t, f)

	_, err := mintWith(t, map[string]string{"client_id": "Iv1", "private_key": pemStr})
	if err == nil || !strings.Contains(err.Error(), "another-org, gemsdevunited") || !strings.Contains(err.Error(), "set owner") {
		t.Fatalf("expected ambiguity error listing owners, got %v", err)
	}
}

func TestMintGitHubAppToken_ReinstallReResolves(t *testing.T) {
	key, pemStr := testGitHubAppKey(t)
	f := newFakeGitHubAppServer(t, &key.PublicKey, "Iv1")
	withGitHubAppTestServer(t, f)
	fields := map[string]string{"client_id": "Iv1", "private_key": pemStr, "owner": "gemsdevunited"}

	if _, err := mintWith(t, fields); err != nil {
		t.Fatal(err)
	}
	// App uninstalled + reinstalled → new installation id; old id now 404s.
	f.mu.Lock()
	f.installations[0].ID = 4567
	f.liveIDs = map[string]bool{"4567": true}
	f.mu.Unlock()
	githubAppTokenCache = sync.Map{} // force a fresh mint; installation cache kept

	if _, err := mintWith(t, fields); err != nil {
		t.Fatalf("expected transparent re-resolve after reinstall, got %v", err)
	}
	if v, _ := githubAppInstallCache.Load(githubAppCacheKeyFor(t, fields)); v != "4567" {
		t.Fatalf("installation cache not refreshed: %v", v)
	}
}

func githubAppCacheKeyFor(t *testing.T, fields map[string]string) string {
	t.Helper()
	c, err := ParseGitHubAppFields(fields)
	if err != nil {
		t.Fatal(err)
	}
	return githubAppCacheKey("github.com", c)
}
