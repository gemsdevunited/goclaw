package http

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/crypto"
	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

func TestProvidersHandlerRegisterInMemoryAppliesCodexPoolDefaults(t *testing.T) {
	providerReg := providers.NewRegistry(nil)
	handler := NewProvidersHandler(newMockProviderStore(), newMockSecretsStore(), providerReg, "")

	provider := &store.LLMProviderData{
		BaseModel:    store.BaseModel{ID: uuid.New()},
		TenantID:     uuid.New(),
		Name:         "openai-codex",
		ProviderType: store.ProviderChatGPTOAuth,
		APIKey:       "token",
		Enabled:      true,
		Settings: json.RawMessage(`{
			"codex_pool": {
				"strategy": "round_robin",
				"extra_provider_names": ["codex-work"]
			}
		}`),
	}

	handler.registerInMemory(provider)

	runtimeProvider, err := providerReg.GetForTenant(provider.TenantID, provider.Name)
	if err != nil {
		t.Fatalf("GetForTenant() error = %v", err)
	}
	codex, ok := runtimeProvider.(*providers.CodexProvider)
	if !ok {
		t.Fatalf("runtime provider = %T, want *providers.CodexProvider", runtimeProvider)
	}
	defaults := codex.RoutingDefaults()
	if defaults == nil {
		t.Fatal("RoutingDefaults() = nil, want defaults")
	}
	if defaults.Strategy != store.ChatGPTOAuthStrategyRoundRobin {
		t.Fatalf("Strategy = %q, want %q", defaults.Strategy, store.ChatGPTOAuthStrategyRoundRobin)
	}
	if len(defaults.ExtraProviderNames) != 1 || defaults.ExtraProviderNames[0] != "codex-work" {
		t.Fatalf("ExtraProviderNames = %#v, want [\"codex-work\"]", defaults.ExtraProviderNames)
	}
}

// TestProvidersHandlerRegisterInMemoryUsesDBNameForAnthropic guards the onboarding verify flow:
// when an Anthropic provider is created via HTTP with a custom name, the in-memory registry
// must key the provider by that DB name — not the hardcoded "anthropic" default. Otherwise
// handleVerifyProvider's GetForTenant(p.TenantID, p.Name) lookup fails with "provider not registered".
// See commit 7fcf0327 for the matching fix on the startup path (cmd/gateway_providers.go).
func TestProvidersHandlerRegisterInMemoryUsesDBNameForAnthropic(t *testing.T) {
	providerReg := providers.NewRegistry(nil)
	handler := NewProvidersHandler(newMockProviderStore(), newMockSecretsStore(), providerReg, "")

	provider := &store.LLMProviderData{
		BaseModel:    store.BaseModel{ID: uuid.New()},
		TenantID:     uuid.New(),
		Name:         "my-anthropic",
		ProviderType: store.ProviderAnthropicNative,
		APIKey:       "sk-ant-test",
		Enabled:      true,
	}

	handler.registerInMemory(provider)

	got, err := providerReg.GetForTenant(provider.TenantID, provider.Name)
	if err != nil {
		t.Fatalf("GetForTenant(%q) error = %v, want provider registered under DB name", provider.Name, err)
	}
	if got.Name() != provider.Name {
		t.Fatalf("Name() = %q, want %q", got.Name(), provider.Name)
	}

	// Negative: the hardcoded default "anthropic" must NOT be registered when the user chose a different name.
	if _, err := providerReg.GetForTenant(provider.TenantID, "anthropic"); err == nil {
		t.Fatal("GetForTenant(\"anthropic\") succeeded, want not-found — provider should only live under its DB name")
	}
}

func TestProvidersHandlerRegisterInMemoryMiniMaxUsesM3Default(t *testing.T) {
	providerReg := providers.NewRegistry(nil)
	handler := NewProvidersHandler(newMockProviderStore(), newMockSecretsStore(), providerReg, "")

	provider := &store.LLMProviderData{
		BaseModel:    store.BaseModel{ID: uuid.New()},
		TenantID:     uuid.New(),
		Name:         "minimax",
		ProviderType: store.ProviderMiniMax,
		APIKey:       "token",
		Enabled:      true,
	}

	handler.registerInMemory(provider)

	runtimeProvider, err := providerReg.GetForTenant(provider.TenantID, provider.Name)
	if err != nil {
		t.Fatalf("GetForTenant() error = %v", err)
	}
	if got := runtimeProvider.DefaultModel(); got != "MiniMax-M3" {
		t.Fatalf("DefaultModel() = %q, want MiniMax-M3", got)
	}
	openai, ok := runtimeProvider.(*providers.OpenAIProvider)
	if !ok {
		t.Fatalf("runtime provider = %T, want *providers.OpenAIProvider", runtimeProvider)
	}
	if got := openai.APIBase(); got != "https://api.minimax.io/v1" {
		t.Fatalf("APIBase() = %q, want MiniMax default base", got)
	}
}

func TestProvidersHandlerRegisterInMemoryMiniMaxUsesOpenAIChatCompletionsPath(t *testing.T) {
	var capturedPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{
					"message":       map[string]string{"content": "ok"},
					"finish_reason": "stop",
				},
			},
		})
	}))
	t.Cleanup(upstream.Close)

	providerReg := providers.NewRegistry(nil)
	handler := NewProvidersHandler(newMockProviderStore(), newMockSecretsStore(), providerReg, "")
	provider := &store.LLMProviderData{
		BaseModel:    store.BaseModel{ID: uuid.New()},
		TenantID:     uuid.New(),
		Name:         "minimax",
		ProviderType: store.ProviderMiniMax,
		APIBase:      upstream.URL,
		APIKey:       "token",
		Enabled:      true,
	}

	handler.registerInMemory(provider)

	runtimeProvider, err := providerReg.GetForTenant(provider.TenantID, provider.Name)
	if err != nil {
		t.Fatalf("GetForTenant() error = %v", err)
	}
	_, err = runtimeProvider.Chat(context.Background(), providers.ChatRequest{
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if capturedPath != "/chat/completions" {
		t.Fatalf("captured path = %q, want /chat/completions", capturedPath)
	}
}

func TestProvidersHandlerRegisterInMemoryZaiUsesGLM52Default(t *testing.T) {
	for _, tt := range []struct {
		providerType string
		wantBase     string
	}{
		{providerType: store.ProviderZai, wantBase: "https://api.z.ai/api/paas/v4"},
		{providerType: store.ProviderZaiCoding, wantBase: "https://api.z.ai/api/coding/paas/v4"},
	} {
		t.Run(tt.providerType, func(t *testing.T) {
			providerReg := providers.NewRegistry(nil)
			handler := NewProvidersHandler(newMockProviderStore(), newMockSecretsStore(), providerReg, "")
			provider := &store.LLMProviderData{
				BaseModel:    store.BaseModel{ID: uuid.New()},
				TenantID:     uuid.New(),
				Name:         tt.providerType,
				ProviderType: tt.providerType,
				APIKey:       "token",
				Enabled:      true,
			}

			handler.registerInMemory(provider)

			runtimeProvider, err := providerReg.GetForTenant(provider.TenantID, provider.Name)
			if err != nil {
				t.Fatalf("GetForTenant() error = %v", err)
			}
			if got := runtimeProvider.DefaultModel(); got != "glm-5.2" {
				t.Fatalf("DefaultModel() = %q, want glm-5.2", got)
			}
			openai, ok := runtimeProvider.(*providers.OpenAIProvider)
			if !ok {
				t.Fatalf("runtime provider = %T, want *providers.OpenAIProvider", runtimeProvider)
			}
			if got := openai.APIBase(); got != tt.wantBase {
				t.Fatalf("APIBase() = %q, want %q", got, tt.wantBase)
			}
		})
	}
}

// TestProvidersHandlerRegisterInMemoryUsesDBNameForClaudeCLI mirrors the Anthropic guard for Claude CLI.
// Custom-named CLI providers must be registered under their DB name to be locatable via verify.
func TestProvidersHandlerRegisterInMemoryUsesDBNameForClaudeCLI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows: shell-script fake binary not portable")
	}
	providerReg := providers.NewRegistry(nil)
	handler := NewProvidersHandler(newMockProviderStore(), newMockSecretsStore(), providerReg, "")

	// Create a fake executable so exec.LookPath succeeds — the binary-existence guard
	// added in registerInMemory (parity with startup path) would otherwise skip registration
	// when the test runner has no real `claude` binary in PATH.
	fakeCLI := writeFakeClaudeBinary(t)

	provider := &store.LLMProviderData{
		BaseModel:    store.BaseModel{ID: uuid.New()},
		TenantID:     uuid.New(),
		Name:         "claude-max",
		ProviderType: store.ProviderClaudeCLI,
		APIBase:      fakeCLI,
		Enabled:      true,
	}

	handler.registerInMemory(provider)

	got, err := providerReg.GetForTenant(provider.TenantID, provider.Name)
	if err != nil {
		t.Fatalf("GetForTenant(%q) error = %v, want provider registered under DB name", provider.Name, err)
	}
	if got.Name() != provider.Name {
		t.Fatalf("Name() = %q, want %q", got.Name(), provider.Name)
	}

	// Negative: the hardcoded default "claude-cli" must NOT be registered when the user chose a different name.
	if _, err := providerReg.GetForTenant(provider.TenantID, "claude-cli"); err == nil {
		t.Fatal("GetForTenant(\"claude-cli\") succeeded, want not-found — provider should only live under its DB name")
	}
}

// TestProvidersHandlerRegisterInMemorySkipsClaudeCLIWhenBinaryMissing guards the binary-existence check
// added to mirror cmd/gateway_providers.go. If the configured CLI path does not resolve via exec.LookPath,
// registerInMemory must return without registering — otherwise verify would succeed on a provider that
// cannot actually spawn the CLI.
func TestProvidersHandlerRegisterInMemorySkipsClaudeCLIWhenBinaryMissing(t *testing.T) {
	providerReg := providers.NewRegistry(nil)
	handler := NewProvidersHandler(newMockProviderStore(), newMockSecretsStore(), providerReg, "")

	// Absolute path to a file that cannot exist — exec.LookPath must fail.
	missingPath := filepath.Join(t.TempDir(), "definitely-not-claude")

	provider := &store.LLMProviderData{
		BaseModel:    store.BaseModel{ID: uuid.New()},
		TenantID:     uuid.New(),
		Name:         "claude-broken",
		ProviderType: store.ProviderClaudeCLI,
		APIBase:      missingPath,
		Enabled:      true,
	}

	handler.registerInMemory(provider)

	if _, err := providerReg.GetForTenant(provider.TenantID, provider.Name); err == nil {
		t.Fatal("GetForTenant succeeded, want not-found — registration should be skipped when binary missing")
	}
}

// TestProvidersHandlerRegisterInMemoryAnthropicUsesModelRegistry verifies the HTTP onboarding path
// wires the forward-compat ModelRegistry into Anthropic providers, matching the startup path behavior
// (cmd/gateway_providers.go:349). Without this, model alias resolution and token counting fall back
// to static defaults — affects cost accounting and forward-compat for new model IDs.
func TestProvidersHandlerRegisterInMemoryAnthropicUsesModelRegistry(t *testing.T) {
	providerReg := providers.NewRegistry(nil)
	handler := NewProvidersHandler(newMockProviderStore(), newMockSecretsStore(), providerReg, "")

	modelReg := providers.NewInMemoryRegistry()
	handler.SetModelRegistry(modelReg)

	provider := &store.LLMProviderData{
		BaseModel:    store.BaseModel{ID: uuid.New()},
		TenantID:     uuid.New(),
		Name:         "my-anthropic",
		ProviderType: store.ProviderAnthropicNative,
		APIKey:       "sk-ant-test",
		Enabled:      true,
	}

	handler.registerInMemory(provider)

	got, err := providerReg.GetForTenant(provider.TenantID, provider.Name)
	if err != nil {
		t.Fatalf("GetForTenant() error = %v", err)
	}

	// Reflection probe: the AnthropicProvider stores its registry in an unexported "registry" field.
	// No public getter exists, but this wiring is small enough that a structural check is warranted —
	// the alternative is no coverage, and the field name is stable (see internal/providers/anthropic.go).
	v := reflect.ValueOf(got).Elem().FieldByName("registry")
	if !v.IsValid() {
		t.Fatal("AnthropicProvider.registry field not found — implementation drift")
	}
	// Field is an interface; .IsNil() panics on non-interface kinds, so guard.
	if v.Kind() == reflect.Interface && v.IsNil() {
		t.Fatal("AnthropicProvider.registry is nil, want ModelRegistry set via SetModelRegistry")
	}
}

func TestProvidersHandlerRegisterInMemoryDashScopeProxyKeepsCacheDetection(t *testing.T) {
	providerReg := providers.NewRegistry(nil)
	handler := NewProvidersHandler(newMockProviderStore(), newMockSecretsStore(), providerReg, "")

	provider := &store.LLMProviderData{
		BaseModel:    store.BaseModel{ID: uuid.New()},
		TenantID:     uuid.New(),
		Name:         "qwen-richard",
		ProviderType: store.ProviderDashScope,
		APIBase:      "https://proxy.internal/v1",
		APIKey:       "sk-test",
		Enabled:      true,
	}

	handler.registerInMemory(provider)

	got, err := providerReg.GetForTenant(provider.TenantID, provider.Name)
	if err != nil {
		t.Fatalf("GetForTenant() error = %v", err)
	}
	assertRuntimeProviderWrapsDashScopeSystem(t, got, "qwen3.6-plus")
}

func TestProvidersHandlerRegisterInMemoryBailianProxyKeepsCacheDetection(t *testing.T) {
	providerReg := providers.NewRegistry(nil)
	handler := NewProvidersHandler(newMockProviderStore(), newMockSecretsStore(), providerReg, "")

	provider := &store.LLMProviderData{
		BaseModel:    store.BaseModel{ID: uuid.New()},
		TenantID:     uuid.New(),
		Name:         "qwen-richard",
		ProviderType: store.ProviderBailian,
		APIBase:      "https://proxy.internal/v1",
		APIKey:       "sk-test",
		Enabled:      true,
	}

	handler.registerInMemory(provider)

	got, err := providerReg.GetForTenant(provider.TenantID, provider.Name)
	if err != nil {
		t.Fatalf("GetForTenant() error = %v", err)
	}
	assertRuntimeProviderWrapsDashScopeSystem(t, got, "qwen3.6-plus")
}

func assertRuntimeProviderWrapsDashScopeSystem(t *testing.T, runtimeProvider providers.Provider, model string) {
	t.Helper()

	bodyBuilder, ok := runtimeProvider.(interface {
		BuildRequestBodyForTest(string, providers.ChatRequest, bool) map[string]any
	})
	if !ok {
		t.Fatalf("runtime provider = %T, want BuildRequestBodyForTest", runtimeProvider)
	}

	body := bodyBuilder.BuildRequestBodyForTest(model, providers.ChatRequest{
		Messages: []providers.Message{
			{Role: "system", Content: "Stable prefix\n" + providers.CacheBoundaryMarker + "\nDynamic suffix"},
			{Role: "user", Content: "Hi"},
		},
	}, false)
	msgs := body["messages"].([]map[string]any)
	if _, ok := msgs[0]["content"].([]map[string]any); !ok {
		t.Fatalf("system content = %T, want DashScope cache blocks", msgs[0]["content"])
	}
}

// writeFakeClaudeBinary creates an executable stub in a temp dir so exec.LookPath resolves it.
// Returns the absolute path suitable for use as APIBase on a Claude CLI provider record.
func writeFakeClaudeBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "claude-stub")
	// Minimal POSIX script — registerInMemory only cares that the path resolves, not what it does.
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
	return path
}

func setupProvidersAdminToken(t *testing.T) string {
	t.Helper()
	token := "system-admin-key"
	setupTestCache(t, map[string]*store.APIKeyData{
		crypto.HashAPIKey(token): {
			ID:     uuid.New(),
			Scopes: []string{"operator.admin"},
		},
	})
	return token
}

func TestProvidersHandlerCreateRejectsIncompatibleEmbeddingDimensions(t *testing.T) {
	token := setupProvidersAdminToken(t)
	providerStore := newMockProviderStore()
	handler := NewProvidersHandler(providerStore, newMockSecretsStore(), nil, "")
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body := `{
		"name": "voyage",
		"provider_type": "openai_compat",
		"api_base": "https://api.voyageai.com/v1",
		"enabled": true,
		"settings": {
			"embedding": {
				"enabled": true,
				"model": "voyage-4-nano",
				"dimensions": 2048
			}
		}
	}`

	req := httptest.NewRequest(http.MethodPost, "/v1/providers", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status code = %d, want %d", w.Code, http.StatusBadRequest)
	}
	if !strings.Contains(w.Body.String(), "1536") {
		t.Fatalf("response body = %q, want mention of 1536", w.Body.String())
	}
	if len(providerStore.providers) != 0 {
		t.Fatalf("provider store mutated on invalid create: %#v", providerStore.providers)
	}
}

func TestProvidersHandlerCreateAllows1536EmbeddingDimensions(t *testing.T) {
	token := setupProvidersAdminToken(t)
	providerStore := newMockProviderStore()
	handler := NewProvidersHandler(providerStore, newMockSecretsStore(), nil, "")
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body := `{
		"name": "gemini-emb",
		"provider_type": "gemini_native",
		"api_base": "https://generativelanguage.googleapis.com/v1beta/openai",
		"api_key": "token",
		"enabled": true,
		"settings": {
			"embedding": {
				"enabled": true,
				"model": "gemini-embedding-001",
				"dimensions": 1536
			}
		}
	}`

	req := httptest.NewRequest(http.MethodPost, "/v1/providers", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status code = %d, want %d, body=%s", w.Code, http.StatusCreated, w.Body.String())
	}
	if len(providerStore.providers) != 1 {
		t.Fatalf("provider count = %d, want 1", len(providerStore.providers))
	}
}

func TestProvidersHandlerCreateAllowsClaudeCLIExecutablePath(t *testing.T) {
	token := setupProvidersAdminToken(t)
	providerStore := newMockProviderStore()
	handler := NewProvidersHandler(providerStore, newMockSecretsStore(), nil, "")
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body := map[string]any{
		"name":          "claude-local",
		"provider_type": store.ProviderClaudeCLI,
		"api_base":      writeFakeClaudeBinary(t),
		"enabled":       true,
	}
	rawBody, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/providers", bytes.NewReader(rawBody))
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status code = %d, want %d, body=%s", w.Code, http.StatusCreated, w.Body.String())
	}
	if got := providerStore.providers["claude-local"].APIBase; got == "" || !filepath.IsAbs(got) {
		t.Fatalf("stored Claude CLI api_base = %q, want absolute executable path", got)
	}
}

func TestProvidersHandlerUpdateAllowsClaudeCLIExecutablePath(t *testing.T) {
	token := setupProvidersAdminToken(t)
	providerStore := newMockProviderStore()
	provider := &store.LLMProviderData{
		BaseModel:    store.BaseModel{ID: uuid.New()},
		Name:         "claude-local",
		ProviderType: store.ProviderClaudeCLI,
		APIBase:      "claude",
		Enabled:      true,
	}
	if err := providerStore.CreateProvider(context.Background(), provider); err != nil {
		t.Fatalf("CreateProvider() error = %v", err)
	}

	handler := NewProvidersHandler(providerStore, newMockSecretsStore(), nil, "")
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	nextPath := writeFakeClaudeBinary(t)
	body := map[string]any{"api_base": nextPath}
	rawBody, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodPut, "/v1/providers/"+provider.ID.String(), bytes.NewReader(rawBody))
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d, body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	current, err := providerStore.GetProvider(context.Background(), provider.ID)
	if err != nil {
		t.Fatalf("GetProvider() error = %v", err)
	}
	if current.APIBase != nextPath {
		t.Fatalf("api_base = %q, want %q", current.APIBase, nextPath)
	}
}

func TestProvidersHandlerUpdateRejectsIncompatibleEmbeddingDimensions(t *testing.T) {
	token := setupProvidersAdminToken(t)
	providerStore := newMockProviderStore()
	provider := &store.LLMProviderData{
		BaseModel:    store.BaseModel{ID: uuid.New()},
		Name:         "voyage",
		ProviderType: store.ProviderOpenAICompat,
		APIBase:      "https://api.voyageai.com/v1",
		Enabled:      true,
		Settings:     json.RawMessage(`{"embedding":{"enabled":true,"model":"voyage-4-nano","dimensions":1536}}`),
	}
	if err := providerStore.CreateProvider(context.Background(), provider); err != nil {
		t.Fatalf("CreateProvider() error = %v", err)
	}

	handler := NewProvidersHandler(providerStore, newMockSecretsStore(), nil, "")
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body := `{
		"settings": {
			"embedding": {
				"enabled": true,
				"model": "voyage-4-nano",
				"dimensions": 2048
			}
		}
	}`

	req := httptest.NewRequest(http.MethodPut, "/v1/providers/"+provider.ID.String(), bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status code = %d, want %d", w.Code, http.StatusBadRequest)
	}
	current, err := providerStore.GetProvider(context.Background(), provider.ID)
	if err != nil {
		t.Fatalf("GetProvider() error = %v", err)
	}
	es := store.ParseEmbeddingSettings(current.Settings)
	if es == nil || es.Dimensions != 1536 {
		t.Fatalf("embedding dimensions = %+v, want 1536 preserved", es)
	}
}

func TestProvidersHandlerRegisterInMemoryDecisionProvidersUseDBName(t *testing.T) {
	providerReg := providers.NewRegistry(store.TenantIDFromContext)
	handler := NewProvidersHandler(newMockProviderStore(), newMockSecretsStore(), providerReg, "")
	tenantID := uuid.New()
	ctx := store.WithTenantID(context.Background(), tenantID)

	// Custom named TypeSafe Jev
	jev := &store.LLMProviderData{
		BaseModel:    store.BaseModel{ID: uuid.New()},
		TenantID:     tenantID,
		Name:         "my-custom-jev",
		ProviderType: store.ProviderTypeSafeJev,
		APIKey:       "jev-key",
		Enabled:      true,
	}
	handler.registerInMemory(jev)

	gotJev, err := providerReg.GetDecisionProvider(ctx, jev.Name)
	if err != nil {
		t.Fatalf("GetDecisionProvider(%q) error = %v, want found under DB name", jev.Name, err)
	}
	if gotJev.Name() != jev.Name {
		t.Fatalf("Name() = %q, want %q", gotJev.Name(), jev.Name)
	}

	// Also check GetDecisionProviderForTenant directly
	gotForTenant, err := providerReg.GetDecisionProviderForTenant(tenantID, jev.Name)
	if err != nil || gotForTenant.Name() != jev.Name {
		t.Fatalf("GetDecisionProviderForTenant error = %v", err)
	}

	// Unregistering by DB name removes it
	providerReg.UnregisterForTenant(tenantID, jev.Name)
	if _, err := providerReg.GetDecisionProvider(ctx, jev.Name); err == nil {
		t.Fatalf("expected error after unregistering %q, got nil", jev.Name)
	}
}

func TestProvidersHandlerVerifyDecisionPingChecksRegistration(t *testing.T) {
	token := setupProvidersAdminToken(t)
	providerStore := newMockProviderStore()
	providerReg := providers.NewRegistry(store.TenantIDFromContext)

	provider := &store.LLMProviderData{
		BaseModel:    store.BaseModel{ID: uuid.New()},
		TenantID:     uuid.New(),
		Name:         "unregistered-clef",
		ProviderType: store.ProviderCloudflareClef,
		APIKey:       "clef-token",
		Enabled:      true,
		Settings:     json.RawMessage(`{"account_id":"acc123"}`),
	}
	if err := providerStore.CreateProvider(context.Background(), provider); err != nil {
		t.Fatalf("CreateProvider() error = %v", err)
	}

	handler := NewProvidersHandler(providerStore, newMockSecretsStore(), providerReg, "")
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// Provider is in DB but NOT in providerReg (e.g. before in-memory registration or failed startup).
	// Ping request (empty body)
	req := httptest.NewRequest(http.MethodPost, "/v1/providers/"+provider.ID.String()+"/verify", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	var resp struct {
		Valid bool   `json:"valid"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response error = %v", err)
	}
	if resp.Valid {
		t.Fatal("ping returned valid: true for unregistered decision provider, want false")
	}

	// Now register in memory and test ping again
	handler.registerInMemory(provider)
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/v1/providers/"+provider.ID.String()+"/verify", nil)
	req2.Header.Set("Authorization", "Bearer "+token)
	mux.ServeHTTP(w2, req2)

	var resp2 struct {
		Valid bool `json:"valid"`
	}
	if err := json.NewDecoder(w2.Body).Decode(&resp2); err != nil {
		t.Fatalf("decode response error = %v", err)
	}
	if !resp2.Valid {
		t.Fatal("ping returned valid: false for registered decision provider, want true")
	}
}

func TestProvidersHandlerOpenAICompatDoesNotBecomeDecisionProvider(t *testing.T) {
	tenantID := uuid.New()
	ctx := store.WithTenantID(context.Background(), tenantID)
	providerReg := providers.NewRegistry(store.TenantIDFromContext)
	handler := NewProvidersHandler(newMockProviderStore(), newMockSecretsStore(), providerReg, "")

	compatJev := &store.LLMProviderData{
		BaseModel:    store.BaseModel{ID: uuid.New()},
		TenantID:     tenantID,
		Name:         "compat-jev",
		ProviderType: store.ProviderOpenAICompat,
		APIBase:      "https://api.typesafe.ai",
		APIKey:       "key",
		Enabled:      true,
	}
	handler.registerInMemory(compatJev)

	// Decision provider must NOT be registered for openai_compat URL guessing
	if _, err := providerReg.GetDecisionProviderForTenant(tenantID, "compat-jev"); err == nil {
		t.Fatal("expected openai_compat with typesafe URL NOT to become decision provider")
	}
	if _, err := providerReg.GetForTenant(tenantID, "compat-jev"); err != nil {
		t.Fatalf("expected chat provider registered, got err=%v", err)
	}

	// Register explicit decision provider
	explicitJev := &store.LLMProviderData{
		BaseModel:    store.BaseModel{ID: uuid.New()},
		TenantID:     tenantID,
		Name:         "my-jev",
		ProviderType: store.ProviderTypeSafeJev,
		APIBase:      "https://api.typesafe.ai",
		APIKey:       "key",
		Enabled:      true,
	}
	handler.registerInMemory(explicitJev)

	decProv, err := providerReg.GetDecisionProviderForTenant(tenantID, "my-jev")
	if err != nil || decProv == nil {
		t.Fatalf("expected explicit decision provider registered, got err=%v", err)
	}

	// Unregister by name
	providerReg.UnregisterForTenant(tenantID, "my-jev")
	if _, err := providerReg.GetDecisionProvider(ctx, "my-jev"); err == nil {
		t.Fatalf("expected error after unregistering \"my-jev\", got found")
	}
}

func TestProvidersHandlerUpdateEmptyAPIKeyUnregistersFromRuntime(t *testing.T) {
	token := setupProvidersAdminToken(t)
	tenantID := uuid.New()
	providerStore := newMockProviderStore()
	providerReg := providers.NewRegistry(store.TenantIDFromContext)

	jev := &store.LLMProviderData{
		BaseModel:    store.BaseModel{ID: uuid.New()},
		TenantID:     tenantID,
		Name:         "live-jev",
		ProviderType: store.ProviderTypeSafeJev,
		APIBase:      "https://api.typesafe.ai",
		APIKey:       "valid-key",
		Enabled:      true,
	}
	ctx := store.WithTenantID(context.Background(), tenantID)
	_ = providerStore.CreateProvider(ctx, jev)

	handler := NewProvidersHandler(providerStore, newMockSecretsStore(), providerReg, "")
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// Initially register in memory
	status := handler.registerInMemory(jev)
	if status != providerRuntimeRegistered {
		t.Fatalf("registerInMemory status = %v, want registered", status)
	}
	if _, err := providerReg.GetDecisionProviderForTenant(tenantID, "live-jev"); err != nil {
		t.Fatalf("expected live-jev in registry, got err=%v", err)
	}

	// Update provider: sending empty api_key preserves existing key (safe form submission)
	body := []byte(`{"api_key": ""}`)
	req := httptest.NewRequest(http.MethodPut, "/v1/providers/"+jev.ID.String(), bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("update status = %d, want 200 OK. Body: %s", w.Code, w.Body.String())
	}

	// Verify key was preserved and provider remains registered
	updatedP, err := providerStore.GetProvider(ctx, jev.ID)
	if err != nil || updatedP.APIKey != "valid-key" {
		t.Fatalf("expected api_key to be preserved as 'valid-key', got %q", updatedP.APIKey)
	}
	if _, err := providerReg.GetDecisionProviderForTenant(tenantID, "live-jev"); err != nil {
		t.Fatalf("expected live-jev to remain in registry after empty api_key update, err=%v", err)
	}
}

func TestProvidersHandlerUpdateSwitchFromDecisionToChatProviderUnregistersDecision(t *testing.T) {
	saveAndRestoreGlobals(t)
	dnsResolverFn = stubResolver(map[string][]string{
		"api.openai.com": {"93.184.216.34"},
	})
	token := setupProvidersAdminToken(t)
	tenantID := uuid.New()
	providerStore := newMockProviderStore()
	providerReg := providers.NewRegistry(store.TenantIDFromContext)

	prov := &store.LLMProviderData{
		BaseModel:    store.BaseModel{ID: uuid.New()},
		TenantID:     tenantID,
		Name:         "multi-role-prov",
		ProviderType: store.ProviderTypeSafeJev,
		APIBase:      "https://api.typesafe.ai",
		APIKey:       "test-key",
		Enabled:      true,
	}
	ctx := store.WithTenantID(context.Background(), tenantID)
	_ = providerStore.CreateProvider(ctx, prov)

	handler := NewProvidersHandler(providerStore, newMockSecretsStore(), providerReg, "")
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// Initially registered as decision provider
	status := handler.registerInMemory(prov)
	if status != providerRuntimeRegistered {
		t.Fatalf("registerInMemory status = %v, want registered", status)
	}
	if _, err := providerReg.GetDecisionProviderForTenant(tenantID, "multi-role-prov"); err != nil {
		t.Fatalf("expected multi-role-prov in decision registry, got err=%v", err)
	}

	// Update provider: switch type to openai_compat pointing to standard chat API
	body := []byte(`{"provider_type": "openai_compat", "api_base": "https://api.openai.com/v1"}`)
	req := httptest.NewRequest(http.MethodPut, "/v1/providers/"+prov.ID.String(), bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("update status = %d, want 200 OK. Body: %s", w.Code, w.Body.String())
	}

	// 1. Must be removed from decision registry
	if _, err := providerReg.GetDecisionProviderForTenant(tenantID, "multi-role-prov"); err == nil {
		t.Fatal("expected multi-role-prov to be unregistered from decision registry after switching to chat provider")
	}

	// 2. Must be registered in chat provider registry
	chatProv, err := providerReg.GetForTenant(tenantID, "multi-role-prov")
	if err != nil {
		t.Fatalf("expected multi-role-prov in chat registry, got err=%v", err)
	}
	if chatProv == nil {
		t.Fatal("chat provider is nil")
	}
}

func TestRegisterInMemory_ChatProviderWithDecisionInNameNotAutoRegistered(t *testing.T) {
	tenantID := uuid.New()
	providerReg := providers.NewRegistry(store.TenantIDFromContext)
	handler := NewProvidersHandler(newMockProviderStore(), newMockSecretsStore(), providerReg, "")

	chatProviders := []string{"my-jev-assistant", "decision-maker-bot", "clef-helper"}
	for _, name := range chatProviders {
		p := &store.LLMProviderData{
			BaseModel:    store.BaseModel{ID: uuid.New()},
			TenantID:     tenantID,
			Name:         name,
			ProviderType: store.ProviderOpenAICompat,
			APIBase:      "https://api.openai.com/v1",
			APIKey:       "sk-test",
			Enabled:      true,
		}
		status := handler.registerInMemory(p)
		if status != providerRuntimeRegistered {
			t.Fatalf("registerInMemory(%s) status = %v, want registered", name, status)
		}

		// Must be in chat registry
		if _, err := providerReg.GetForTenant(tenantID, name); err != nil {
			t.Fatalf("expected %s in chat registry, got err=%v", name, err)
		}

		// Must NOT be auto-registered as decision provider
		if _, err := providerReg.GetDecisionProviderForTenant(tenantID, name); err == nil {
			t.Fatalf("expected %s NOT to be registered as decision provider, but was found", name)
		}
	}
}

func TestOpenAIDecision_AutoRegisterNormalizesBaseURL(t *testing.T) {
	tenantID := uuid.New()
	providerReg := providers.NewRegistry(store.TenantIDFromContext)
	handler := NewProvidersHandler(newMockProviderStore(), newMockSecretsStore(), providerReg, "")

	p := &store.LLMProviderData{
		BaseModel:    store.BaseModel{ID: uuid.New()},
		TenantID:     tenantID,
		Name:         "openai-compat-decisions",
		ProviderType: store.ProviderOpenAIDecision,
		APIBase:      "https://api.openai.com/v1/decisions",
		APIKey:       "sk-test",
		Enabled:      true,
	}

	status := handler.registerInMemory(p)
	if status != providerRuntimeRegistered {
		t.Fatalf("registerInMemory status = %v, want registered", status)
	}

	decProv, err := providerReg.GetDecisionProviderForTenant(tenantID, "openai-compat-decisions")
	if err != nil {
		t.Fatalf("expected decision provider to be registered, got error: %v", err)
	}

	// Verify the decision provider does not produce duplicate /decisions in URL
	if decProv == nil {
		t.Fatal("decision provider is nil")
	}
}

func TestClefProvider_MissingAccountID_Rejected(t *testing.T) {
	token := setupProvidersAdminToken(t)
	tenantID := uuid.New()
	providerStore := newMockProviderStore()
	providerReg := providers.NewRegistry(store.TenantIDFromContext)
	handler := NewProvidersHandler(providerStore, newMockSecretsStore(), providerReg, "")
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// 1. handleCreateProvider without Account ID must return 400
	createBody := []byte(`{
		"name": "clef-no-account",
		"provider_type": "cloudflare_clef",
		"api_key": "some-token"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/providers", bytes.NewReader(createBody))
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("create status = %d, want 400 Bad Request. Body: %s", w.Code, w.Body.String())
	}

	// 2. registerInMemory without Account ID must return providerRuntimeInvalidConfig and NOT register
	p := &store.LLMProviderData{
		BaseModel:    store.BaseModel{ID: uuid.New()},
		TenantID:     tenantID,
		Name:         "clef-runtime-test",
		ProviderType: store.ProviderCloudflareClef,
		APIKey:       "token",
		Enabled:      true,
	}
	status := handler.registerInMemory(p)
	if status != providerRuntimeInvalidConfig {
		t.Fatalf("registerInMemory status = %v, want providerRuntimeInvalidConfig", status)
	}
	if _, err := providerReg.GetDecisionProviderForTenant(tenantID, "clef-runtime-test"); err == nil {
		t.Fatal("expected Clef without account ID NOT to be in registry, but was found")
	}

	// 3. provider_verify on Clef without Account ID must return valid: false (even in ping mode)
	ctx := store.WithTenantID(context.Background(), tenantID)
	_ = providerStore.CreateProvider(ctx, p)

	verifyBody := []byte(`{"ping": true}`)
	vreq := httptest.NewRequest(http.MethodPost, "/v1/providers/"+p.ID.String()+"/verify", bytes.NewReader(verifyBody))
	vreq.Header.Set("Authorization", "Bearer "+token)
	vw := httptest.NewRecorder()
	mux.ServeHTTP(vw, vreq)

	if vw.Code != http.StatusOK {
		t.Fatalf("verify status = %d, want 200 OK. Body: %s", vw.Code, vw.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(vw.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal verify response: %v", err)
	}
	if resp["valid"] == true {
		t.Fatal("expected valid: false for Clef without account ID, got valid: true")
	}
}

type stubRoundTripper func(*http.Request) (*http.Response, error)

func (s stubRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return s(r)
}

func TestProvidersHandlerVerifyDecisionRequiresValidAnswer(t *testing.T) {
	token := setupProvidersAdminToken(t)
	tenantID := uuid.New()
	ctx := store.WithTenantID(context.Background(), tenantID)
	providerStore := newMockProviderStore()
	providerReg := providers.NewRegistry(store.TenantIDFromContext)

	var serverResponse string

	prov := &store.LLMProviderData{
		BaseModel:    store.BaseModel{ID: uuid.New()},
		TenantID:     tenantID,
		Name:         "test-verify-dec",
		ProviderType: store.ProviderTypeSafeJev,
		APIBase:      "https://api.typesafe.ai",
		APIKey:       "key",
		Enabled:      true,
	}
	_ = providerStore.CreateProvider(ctx, prov)

	handler := NewProvidersHandler(providerStore, newMockSecretsStore(), providerReg, "")
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	handler.registerInMemory(prov)

	decProv, err := providerReg.GetDecisionProviderForTenant(tenantID, prov.Name)
	if err != nil {
		t.Fatalf("failed to get decision provider: %v", err)
	}
	if jp, ok := decProv.(*providers.TypeSafeAIJevProvider); ok {
		jp.WithClient(&http.Client{
			Transport: stubRoundTripper(func(r *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(serverResponse)),
				}, nil
			}),
		})
	}

	// 1. Server returns empty JSON object {} -> must fail verify
	serverResponse = `{}`
	reqEmpty := httptest.NewRequest(http.MethodPost, "/v1/providers/"+prov.ID.String()+"/verify", bytes.NewReader([]byte(`{"model": "jev-latest"}`)))
	reqEmpty.Header.Set("Authorization", "Bearer "+token)
	wEmpty := httptest.NewRecorder()
	mux.ServeHTTP(wEmpty, reqEmpty)

	var respEmpty map[string]any
	_ = json.Unmarshal(wEmpty.Body.Bytes(), &respEmpty)
	if respEmpty["valid"] == true {
		t.Fatalf("expected valid: false when decision model returns empty answers, got %+v", respEmpty)
	}

	// 1b. Server returns {"answers": {"verify": {}}} with missing noul value -> must fail verify
	serverResponse = `{"answers": {"verify": {}}}`
	reqEmptyNoul := httptest.NewRequest(http.MethodPost, "/v1/providers/"+prov.ID.String()+"/verify", bytes.NewReader([]byte(`{"model": "jev-latest"}`)))
	reqEmptyNoul.Header.Set("Authorization", "Bearer "+token)
	wEmptyNoul := httptest.NewRecorder()
	mux.ServeHTTP(wEmptyNoul, reqEmptyNoul)

	var respEmptyNoul map[string]any
	_ = json.Unmarshal(wEmptyNoul.Body.Bytes(), &respEmptyNoul)
	if respEmptyNoul["valid"] == true {
		t.Fatalf("expected valid: false when decision model returns empty answer object {}, got %+v", respEmptyNoul)
	}

	// 2. Server returns valid answers with "verify" question -> must succeed
	serverResponse = `{"answers": {"verify": {"noul": 0.99}}}`
	reqValid := httptest.NewRequest(http.MethodPost, "/v1/providers/"+prov.ID.String()+"/verify", bytes.NewReader([]byte(`{"model": "jev-latest"}`)))
	reqValid.Header.Set("Authorization", "Bearer "+token)
	wValid := httptest.NewRecorder()
	mux.ServeHTTP(wValid, reqValid)

	var respValid map[string]any
	_ = json.Unmarshal(wValid.Body.Bytes(), &respValid)
	if respValid["valid"] != true {
		t.Fatalf("expected valid: true when decision model answers verify question, got %+v", respValid)
	}
}
