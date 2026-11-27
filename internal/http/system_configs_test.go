package http

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

type mockSystemConfigStoreForTest struct {
	configs map[string]string
}

func newMockSystemConfigStoreForTest() *mockSystemConfigStoreForTest {
	return &mockSystemConfigStoreForTest{configs: make(map[string]string)}
}

func (m *mockSystemConfigStoreForTest) Get(ctx context.Context, key string) (string, error) {
	if val, ok := m.configs[key]; ok {
		return val, nil
	}
	return "", errors.New("not found")
}

func (m *mockSystemConfigStoreForTest) Set(ctx context.Context, key, value string) error {
	m.configs[key] = value
	return nil
}

func (m *mockSystemConfigStoreForTest) Delete(ctx context.Context, key string) error {
	delete(m.configs, key)
	return nil
}

func (m *mockSystemConfigStoreForTest) List(ctx context.Context) (map[string]string, error) {
	cp := make(map[string]string, len(m.configs))
	for k, v := range m.configs {
		cp[k] = v
	}
	return cp, nil
}

func TestSystemConfigsHandler_DecisionProviderValidation(t *testing.T) {
	token := setupProvidersAdminToken(t)
	tenantID := uuid.New()
	ctx := store.WithTenantID(context.Background(), tenantID)

	providerStore := newMockProviderStore()
	sysConfigStore := newMockSystemConfigStoreForTest()

	// 1. Create a valid, enabled decision provider
	jev := &store.LLMProviderData{
		BaseModel:    store.BaseModel{ID: uuid.New()},
		TenantID:     tenantID,
		Name:         "my-jev",
		ProviderType: store.ProviderTypeSafeJev,
		APIKey:       "key",
		Enabled:      true,
	}
	_ = providerStore.CreateProvider(ctx, jev)

	// 2. Create a disabled decision provider
	disabledClef := &store.LLMProviderData{
		BaseModel:    store.BaseModel{ID: uuid.New()},
		TenantID:     tenantID,
		Name:         "disabled-clef",
		ProviderType: store.ProviderCloudflareClef,
		APIKey:       "key",
		Enabled:      false,
	}
	_ = providerStore.CreateProvider(ctx, disabledClef)

	// 3. Create a chat provider (not a decision provider)
	chatProv := &store.LLMProviderData{
		BaseModel:    store.BaseModel{ID: uuid.New()},
		TenantID:     tenantID,
		Name:         "openai-chat",
		ProviderType: store.ProviderOpenAICompat,
		APIKey:       "key",
		Enabled:      true,
	}
	_ = providerStore.CreateProvider(ctx, chatProv)

	handler := NewSystemConfigsHandler(sysConfigStore, nil).WithProviderStore(providerStore)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	setKey := func(val string) (int, string) {
		body := []byte(`{"value": "` + val + `"}`)
		req := httptest.NewRequest(http.MethodPut, "/v1/system-configs/decision.provider", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}

	// Case A: Non-existent provider -> 400
	code, body := setKey("ghost-provider")
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400 for non-existent provider, got %d. Body: %s", code, body)
	}

	// Case B: Disabled provider -> 400
	code, body = setKey("disabled-clef")
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400 for disabled provider, got %d. Body: %s", code, body)
	}

	// Case C: Chat provider (not decision capability) -> 400
	code, body = setKey("openai-chat")
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400 for chat provider, got %d. Body: %s", code, body)
	}

	// Case D: Valid decision provider -> 200 OK
	code, body = setKey("my-jev")
	if code != http.StatusOK {
		t.Fatalf("expected 200 for valid decision provider, got %d. Body: %s", code, body)
	}
	if sysConfigStore.configs["decision.provider"] != "my-jev" {
		t.Fatalf("expected stored value 'my-jev', got %q", sysConfigStore.configs["decision.provider"])
	}

	// Case E: Empty string (disable decision provider) -> 200 OK
	code, body = setKey("")
	if code != http.StatusOK {
		t.Fatalf("expected 200 for empty string, got %d. Body: %s", code, body)
	}
	if sysConfigStore.configs["decision.provider"] != "" {
		t.Fatalf("expected stored value '', got %q", sysConfigStore.configs["decision.provider"])
	}

	// Case F: Delete key -> 200 OK
	req := httptest.NewRequest(http.MethodDelete, "/v1/system-configs/decision.provider", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent && w.Code != http.StatusOK {
		t.Fatalf("expected 200 or 204 for DELETE, got %d. Body: %s", w.Code, w.Body.String())
	}
}
