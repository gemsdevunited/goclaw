package providerresolve

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

type mockSystemConfigStore struct {
	configs map[string]string // key: tenantID.String() + "/" + configKey
}

func newMockSystemConfigStore() *mockSystemConfigStore {
	return &mockSystemConfigStore{configs: make(map[string]string)}
}

func (m *mockSystemConfigStore) Get(ctx context.Context, key string) (string, error) {
	tid := store.TenantIDFromContext(ctx)
	if val, ok := m.configs[tid.String()+"/"+key]; ok {
		return val, nil
	}
	return "", errors.New("not found")
}

func (m *mockSystemConfigStore) Set(ctx context.Context, key, value string) error {
	tid := store.TenantIDFromContext(ctx)
	m.configs[tid.String()+"/"+key] = value
	return nil
}

func (m *mockSystemConfigStore) Delete(ctx context.Context, key string) error {
	tid := store.TenantIDFromContext(ctx)
	delete(m.configs, tid.String()+"/"+key)
	return nil
}

func (m *mockSystemConfigStore) List(ctx context.Context) (map[string]string, error) {
	tid := store.TenantIDFromContext(ctx)
	prefix := tid.String() + "/"
	res := make(map[string]string)
	for k, v := range m.configs {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			res[k[len(prefix):]] = v
		}
	}
	return res, nil
}

func TestBuildDecisionProvider(t *testing.T) {
	tenantID := uuid.New()

	// 1. Clef missing account ID
	badClef := &store.LLMProviderData{
		TenantID:     tenantID,
		Name:         "clef-bad",
		ProviderType: store.ProviderCloudflareClef,
		APIKey:       "token-123",
	}
	_, err := BuildDecisionProvider(badClef)
	if err == nil {
		t.Fatal("expected error for Clef without account ID")
	}

	// 2. Clef with account ID in settings
	goodClef := &store.LLMProviderData{
		TenantID:     tenantID,
		Name:         "clef-good",
		ProviderType: store.ProviderCloudflareClef,
		APIKey:       "token-123",
		Settings:     json.RawMessage(`{"account_id": "acc-456", "model": "clef-fast"}`),
	}
	clefProv, err := BuildDecisionProvider(goodClef)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if clefProv == nil || clefProv.Name() != "clef-good" {
		t.Fatalf("expected Clef provider with name clef-good, got %v", clefProv)
	}

	// 3. Jev provider
	jev := &store.LLMProviderData{
		TenantID:     tenantID,
		Name:         "my-jev",
		ProviderType: store.ProviderTypeSafeJev,
		APIKey:       "jev-key",
		APIBase:      "https://api.typesafe.ai",
	}
	jevProv, err := BuildDecisionProvider(jev)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if jevProv == nil || jevProv.Name() != "my-jev" {
		t.Fatalf("expected Jev provider with name my-jev, got %v", jevProv)
	}

	// 4. OpenAI decision with /decisions suffix normalized
	openaiDec := &store.LLMProviderData{
		TenantID:     tenantID,
		Name:         "my-decisions",
		ProviderType: store.ProviderOpenAIDecision,
		APIKey:       "openai-key",
		APIBase:      "https://api.openai.com/v1/decisions",
	}
	decProv, err := BuildDecisionProvider(openaiDec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decProv == nil || decProv.Name() != "my-decisions" {
		t.Fatalf("expected OpenAI decision provider, got %v", decProv)
	}

	// 5. Non-decision provider returns nil, nil
	chatProv := &store.LLMProviderData{
		TenantID:     tenantID,
		Name:         "gpt-4o",
		ProviderType: store.ProviderOpenAICompat,
		APIKey:       "key",
	}
	nonDec, err := BuildDecisionProvider(chatProv)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if nonDec != nil {
		t.Fatalf("expected nil for chat provider, got %v", nonDec)
	}
}

func TestResolveDecisionProvider(t *testing.T) {
	ctx := context.Background()
	reg := providers.NewRegistry(store.TenantIDFromContext)
	sysConfigs := newMockSystemConfigStore()

	tenantA := uuid.New()
	tenantB := uuid.New()

	// Register a decision provider for tenant A and one for master tenant
	jevA := &store.LLMProviderData{
		TenantID:     tenantA,
		Name:         "jev-a",
		ProviderType: store.ProviderTypeSafeJev,
		APIKey:       "key-a",
	}
	_, _ = RegisterDecisionProvider(reg, jevA)

	masterClef := &store.LLMProviderData{
		TenantID:     store.MasterTenantID,
		Name:         "master-clef",
		ProviderType: store.ProviderCloudflareClef,
		APIKey:       "master-key",
		Settings:     json.RawMessage(`{"account_id": "master-acc"}`),
	}
	_, _ = RegisterDecisionProvider(reg, masterClef)

	// 1. Tenant A has no decision.provider config -> returns nil (chat fallback)
	if p := ResolveDecisionProvider(ctx, tenantA, reg, sysConfigs); p != nil {
		t.Fatalf("expected nil for tenant without decision.provider configured, got %v", p)
	}

	// 2. Tenant B has no config -> returns nil, does NOT inherit master
	if p := ResolveDecisionProvider(ctx, tenantB, reg, sysConfigs); p != nil {
		t.Fatalf("expected nil for tenant B without config, got %v", p)
	}

	// 3. Tenant A configures decision.provider = "jev-a" -> returns jevA
	tAContext := store.WithTenantID(ctx, tenantA)
	_ = sysConfigs.Set(tAContext, DecisionProviderConfigKey, "jev-a")

	resolvedA := ResolveDecisionProvider(ctx, tenantA, reg, sysConfigs)
	if resolvedA == nil || resolvedA.Name() != "jev-a" {
		t.Fatalf("expected jev-a, got %v", resolvedA)
	}

	// 4. Tenant B explicitly configures decision.provider = "master-clef" -> resolves master provider
	tBContext := store.WithTenantID(ctx, tenantB)
	_ = sysConfigs.Set(tBContext, DecisionProviderConfigKey, "master-clef")

	resolvedB := ResolveDecisionProvider(ctx, tenantB, reg, sysConfigs)
	if resolvedB == nil || resolvedB.Name() != "master-clef" {
		t.Fatalf("expected master-clef for tenant B explicit selection, got %v", resolvedB)
	}

	// 5. Tenant A selects a non-existent provider -> returns nil
	_ = sysConfigs.Set(tAContext, DecisionProviderConfigKey, "non-existent")
	if p := ResolveDecisionProvider(ctx, tenantA, reg, sysConfigs); p != nil {
		t.Fatalf("expected nil for non-existent provider, got %v", p)
	}
}

func TestClassifyDecisionError(t *testing.T) {
	tests := []struct {
		err            error
		expectedReason string
		expectedMsg    string
	}{
		{
			err:            nil,
			expectedReason: "ok",
			expectedMsg:    "",
		},
		{
			err:            errors.New("context deadline exceeded"),
			expectedReason: "timeout",
			expectedMsg:    "deadline exceeded",
		},
		{
			err:            errors.New("decision: low confidence 0.35 below 0.6"),
			expectedReason: "low_confidence",
			expectedMsg:    "confidence below threshold",
		},
		{
			err:            errors.New("clef: parse json response error: unexpected end of JSON"),
			expectedReason: "invalid_response",
			expectedMsg:    "malformed or missing answer schema",
		},
		{
			err:            errors.New("jev: missing choice verify in answers"),
			expectedReason: "invalid_response",
			expectedMsg:    "malformed or missing answer schema",
		},
		{
			err:            errors.New("clef: http error 429: Rate limit exceeded for sensitive prompt and key=secret123"),
			expectedReason: "upstream_http_error",
			expectedMsg:    "http error 429",
		},
		{
			err:            errors.New("jev: http error 500: Internal Server Error"),
			expectedReason: "upstream_http_error",
			expectedMsg:    "http error 500",
		},
		{
			err:            errors.New("dial tcp 1.2.3.4:443: connect: connection refused"),
			expectedReason: "network_error",
			expectedMsg:    "connection or network failure",
		},
		{
			err:            errors.New("something completely unexpected happened"),
			expectedReason: "provider_error",
			expectedMsg:    "decision evaluation failed",
		},
	}

	for _, tc := range tests {
		reason, cleanMsg := ClassifyDecisionError(tc.err)
		if reason != tc.expectedReason {
			t.Errorf("for %v: expected reason %q, got %q", tc.err, tc.expectedReason, reason)
		}
		if cleanMsg != tc.expectedMsg {
			t.Errorf("for %v: expected msg %q, got %q", tc.err, tc.expectedMsg, cleanMsg)
		}
	}
}


