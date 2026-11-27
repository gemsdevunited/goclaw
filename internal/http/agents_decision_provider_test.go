package http

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

type mockAgentStoreForValidation struct {
	store.AgentStore
	agents map[uuid.UUID]*store.AgentData
}

func newMockAgentStoreForValidation() *mockAgentStoreForValidation {
	return &mockAgentStoreForValidation{agents: make(map[uuid.UUID]*store.AgentData)}
}

func (m *mockAgentStoreForValidation) Create(_ context.Context, a *store.AgentData) error {
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	m.agents[a.ID] = a
	return nil
}

func (m *mockAgentStoreForValidation) GetByID(_ context.Context, id uuid.UUID) (*store.AgentData, error) {
	if a, ok := m.agents[id]; ok {
		return a, nil
	}
	return nil, errors.New("not found")
}

func (m *mockAgentStoreForValidation) GetByKey(_ context.Context, key string) (*store.AgentData, error) {
	for _, a := range m.agents {
		if a.AgentKey == key {
			return a, nil
		}
	}
	return nil, nil
}

func (m *mockAgentStoreForValidation) List(context.Context, string) ([]store.AgentData, error) {
	var list []store.AgentData
	for _, a := range m.agents {
		list = append(list, *a)
	}
	return list, nil
}

func (m *mockAgentStoreForValidation) ListAccessible(context.Context, string) ([]store.AgentData, error) {
	return m.List(context.Background(), "")
}

func (m *mockAgentStoreForValidation) Update(_ context.Context, id uuid.UUID, updates map[string]any) error {
	a, ok := m.agents[id]
	if !ok {
		return errors.New("not found")
	}
	if p, ok := updates["provider"].(string); ok {
		a.Provider = p
	}
	return nil
}

func (m *mockAgentStoreForValidation) CanAccess(context.Context, uuid.UUID, string) (bool, string, error) {
	return true, "admin", nil
}

func (m *mockAgentStoreForValidation) GetAgentContextFiles(context.Context, uuid.UUID) ([]store.AgentContextFileData, error) {
	return nil, nil
}

func (m *mockAgentStoreForValidation) SetAgentContextFile(context.Context, uuid.UUID, string, string) error {
	return nil
}

func (m *mockAgentStoreForValidation) BulkCreateContextFiles(context.Context, uuid.UUID, []store.AgentContextFileData) error {
	return nil
}

func TestAgentsHandlerRejectsDecisionModelAsAgentChatProvider(t *testing.T) {
	tenantID := uuid.New()
	ctx := store.WithTenantID(context.Background(), tenantID)
	ctx = store.WithUserID(ctx, "admin-user")
	ctx = store.WithRole(ctx, string(permissions.RoleAdmin))

	providerStore := newMockProviderStore()
	agentStore := newMockAgentStoreForValidation()

	// Create a decision provider and a chat provider in store
	decProvider := &store.LLMProviderData{
		BaseModel:    store.BaseModel{ID: uuid.New()},
		TenantID:     tenantID,
		Name:         "my-clef-decision",
		ProviderType: store.ProviderCloudflareClef,
		APIBase:      "https://api.cloudflare.com/client/v4/accounts/acc123",
		APIKey:       "token",
		Enabled:      true,
	}
	chatProvider := &store.LLMProviderData{
		BaseModel:    store.BaseModel{ID: uuid.New()},
		TenantID:     tenantID,
		Name:         "my-openai-chat",
		ProviderType: store.ProviderOpenAICompat,
		APIBase:      "https://api.openai.com/v1",
		APIKey:       "key",
		Enabled:      true,
	}
	_ = providerStore.CreateProvider(ctx, decProvider)
	_ = providerStore.CreateProvider(ctx, chatProvider)

	handler := NewAgentsHandler(agentStore, providerStore, nil, nil, nil, "/tmp", nil, nil, nil)

	// 1. Creating agent with decision provider must fail with 400 Bad Request
	bodyBad := []byte(`{
		"agent_key": "decision-agent",
		"display_name": "Decision Agent",
		"provider": "my-clef-decision",
		"model": "clef-1.1"
	}`)
	reqBad := httptest.NewRequest(http.MethodPost, "/v1/agents", bytes.NewReader(bodyBad)).WithContext(ctx)
	wBad := httptest.NewRecorder()
	handler.handleCreate(wBad, reqBad)

	if wBad.Code != http.StatusBadRequest {
		t.Fatalf("handleCreate status = %d, want 400 Bad Request. Body: %s", wBad.Code, wBad.Body.String())
	}
	if !strings.Contains(wBad.Body.String(), "decision provider") {
		t.Errorf("expected error message mentioning decision provider, got %s", wBad.Body.String())
	}

	// 2. Creating agent with decision provider in model_fallback must fail with 400 Bad Request
	bodyBadFallback := []byte(`{
		"agent_key": "fallback-bad-agent",
		"display_name": "Fallback Bad Agent",
		"provider": "my-openai-chat",
		"model": "gpt-4o",
		"model_fallback": {
			"enabled": true,
			"candidates": [
				{"provider": "my-clef-decision", "model": "clef-1.1"}
			]
		}
	}`)
	reqBadFallback := httptest.NewRequest(http.MethodPost, "/v1/agents", bytes.NewReader(bodyBadFallback)).WithContext(ctx)
	wBadFallback := httptest.NewRecorder()
	handler.handleCreate(wBadFallback, reqBadFallback)

	if wBadFallback.Code != http.StatusBadRequest {
		t.Fatalf("handleCreate with decision fallback status = %d, want 400 Bad Request. Body: %s", wBadFallback.Code, wBadFallback.Body.String())
	}

	// 3. Creating agent with chat provider succeeds
	bodyGood := []byte(`{
		"agent_key": "good-agent",
		"display_name": "Good Agent",
		"provider": "my-openai-chat",
		"model": "gpt-4o"
	}`)
	reqGood := httptest.NewRequest(http.MethodPost, "/v1/agents", bytes.NewReader(bodyGood)).WithContext(ctx)
	wGood := httptest.NewRecorder()
	handler.handleCreate(wGood, reqGood)

	if wGood.Code != http.StatusCreated && wGood.Code != http.StatusOK {
		t.Fatalf("handleCreate status = %d, want 201/200. Body: %s", wGood.Code, wGood.Body.String())
	}

	// 4. Updating existing agent to decision provider must fail with 400
	createdAgent, err := agentStore.GetByKey(ctx, "good-agent")
	if err != nil || createdAgent == nil {
		t.Fatalf("expected created agent, got err=%v", err)
	}

	updateBad := []byte(`{"provider": "my-clef-decision"}`)
	reqUpdateBad := httptest.NewRequest(http.MethodPut, "/v1/agents/"+createdAgent.ID.String(), bytes.NewReader(updateBad)).WithContext(ctx)
	reqUpdateBad.SetPathValue("id", createdAgent.ID.String())
	wUpdateBad := httptest.NewRecorder()
	handler.handleUpdate(wUpdateBad, reqUpdateBad)

	if wUpdateBad.Code != http.StatusBadRequest {
		t.Fatalf("handleUpdate status = %d, want 400 Bad Request. Body: %s", wUpdateBad.Code, wUpdateBad.Body.String())
	}
}

func TestAgentsHandlerRejectsMasterTenantDecisionModelForTenantAgent(t *testing.T) {
	tenantID := uuid.New()
	ctx := store.WithTenantID(context.Background(), tenantID)
	ctx = store.WithUserID(ctx, "tenant-admin")
	ctx = store.WithRole(ctx, string(permissions.RoleAdmin))

	providerStore := newMockProviderStore()
	agentStore := newMockAgentStoreForValidation()

	// Register decision provider under MasterTenantID, NOT the subtenant
	masterDecProvider := &store.LLMProviderData{
		BaseModel:    store.BaseModel{ID: uuid.New()},
		TenantID:     store.MasterTenantID,
		Name:         "master-clef-decision",
		ProviderType: store.ProviderCloudflareClef,
		APIKey:       "token",
		Enabled:      true,
		Settings:     []byte(`{"account_id":"cf-master"}`),
	}
	masterCtx := store.WithTenantID(context.Background(), store.MasterTenantID)
	if err := providerStore.CreateProvider(masterCtx, masterDecProvider); err != nil {
		t.Fatalf("CreateProvider master: %v", err)
	}

	handler := NewAgentsHandler(agentStore, providerStore, nil, nil, nil, "/tmp", nil, nil, nil)

	// Creating subtenant agent with master decision provider must be rejected
	body := []byte(`{
		"agent_key": "subtenant-agent",
		"display_name": "Subtenant Agent",
		"provider": "master-clef-decision",
		"model": "@cf/cloudflare/clef-flash"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/agents", bytes.NewReader(body)).WithContext(ctx)
	w := httptest.NewRecorder()
	handler.handleCreate(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("handleCreate status = %d, want 400 Bad Request. Body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "decision provider") {
		t.Fatalf("expected error mentioning decision provider, got %s", w.Body.String())
	}
}

func TestAgentsHandlerAllowsTenantChatProviderWhenMasterHasSameNameDecisionProvider(t *testing.T) {
	tenantID := uuid.New()
	ctx := store.WithTenantID(context.Background(), tenantID)
	ctx = store.WithUserID(ctx, "tenant-admin")
	ctx = store.WithRole(ctx, string(permissions.RoleAdmin))

	providerStore := newMockProviderStore()
	agentStore := newMockAgentStoreForValidation()

	// 1. Master tenant has a decision provider named "shared-name"
	masterDecProvider := &store.LLMProviderData{
		BaseModel:    store.BaseModel{ID: uuid.New()},
		TenantID:     store.MasterTenantID,
		Name:         "shared-name",
		ProviderType: store.ProviderTypeSafeJev,
		APIKey:       "key",
		Enabled:      true,
	}
	masterCtx := store.WithTenantID(context.Background(), store.MasterTenantID)
	if err := providerStore.CreateProvider(masterCtx, masterDecProvider); err != nil {
		t.Fatalf("CreateProvider master: %v", err)
	}

	// 2. Tenant has a chat provider with the SAME name "shared-name"
	tenantChatProvider := &store.LLMProviderData{
		BaseModel:    store.BaseModel{ID: uuid.New()},
		TenantID:     tenantID,
		Name:         "shared-name",
		ProviderType: store.ProviderOpenAICompat,
		APIKey:       "key",
		Enabled:      true,
	}
	if err := providerStore.CreateProvider(ctx, tenantChatProvider); err != nil {
		t.Fatalf("CreateProvider tenant: %v", err)
	}

	handler := NewAgentsHandler(agentStore, providerStore, nil, nil, nil, "/tmp", nil, nil, nil)

	// Creating subtenant agent with its own chat provider "shared-name" must succeed (NOT falsely blocked)
	body := []byte(`{
		"agent_key": "subtenant-agent-2",
		"display_name": "Subtenant Agent 2",
		"provider": "shared-name",
		"model": "gpt-4o"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/agents", bytes.NewReader(body)).WithContext(ctx)
	w := httptest.NewRecorder()
	handler.handleCreate(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("handleCreate status = %d, want 201 Created. Body: %s", w.Code, w.Body.String())
	}
}
