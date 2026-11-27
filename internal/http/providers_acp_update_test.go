package http

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// closableChatProvider stands in for a runtime-only provider (ACP) registered at startup.
type closableChatProvider struct {
	name   string
	closed bool
}

func (p *closableChatProvider) Chat(context.Context, providers.ChatRequest) (*providers.ChatResponse, error) {
	return nil, nil
}
func (p *closableChatProvider) ChatStream(context.Context, providers.ChatRequest, func(providers.StreamChunk)) (*providers.ChatResponse, error) {
	return nil, nil
}
func (p *closableChatProvider) DefaultModel() string { return "m" }
func (p *closableChatProvider) Name() string         { return p.name }
func (p *closableChatProvider) Close() error         { p.closed = true; return nil }

// Regression: updating an ACP provider (registered only at startup, skipped by registerInMemory)
// must not unregister/close the live runtime instance.
func TestProvidersHandlerUpdateACPKeepsRuntimeInstance(t *testing.T) {
	saveAndRestoreGlobals(t)
	token := setupProvidersAdminToken(t)
	providerStore := newMockProviderStore()
	providerReg := providers.NewRegistry(store.TenantIDFromContext)

	prov := &store.LLMProviderData{
		BaseModel:    store.BaseModel{ID: uuid.New()},
		TenantID:     providers.MasterTenantID,
		Name:         "my-acp",
		ProviderType: store.ProviderACP,
		APIBase:      "claude",
		Enabled:      true,
	}
	ctx := store.WithTenantID(context.Background(), providers.MasterTenantID)
	_ = providerStore.CreateProvider(ctx, prov)

	live := &closableChatProvider{name: "my-acp"}
	providerReg.RegisterForTenant(providers.MasterTenantID, live)

	handler := NewProvidersHandler(providerStore, newMockSecretsStore(), providerReg, "")
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPut, "/v1/providers/"+prov.ID.String(), bytes.NewReader([]byte(`{"display_name":"Renamed"}`)))
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("update status = %d, body: %s", w.Code, w.Body.String())
	}

	got, err := providerReg.GetForTenant(providers.MasterTenantID, "my-acp")
	if err != nil || got != providers.Provider(live) {
		t.Fatalf("expected live ACP instance to stay registered, got %v err=%v", got, err)
	}
	if live.closed {
		t.Fatal("live ACP instance was closed by a metadata-only update")
	}
}
