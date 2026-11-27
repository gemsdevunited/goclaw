package providerresolve

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// DecisionProviderConfigKey is the system_configs key holding the tenant's selected decision provider name.
const DecisionProviderConfigKey = "decision.provider"

// BuildDecisionProvider creates a DecisionProvider instance from a stored LLMProviderData record.
// Returns (nil, nil) if the provider is not a decision provider type.
func BuildDecisionProvider(p *store.LLMProviderData) (providers.DecisionProvider, error) {
	if p == nil {
		return nil, fmt.Errorf("provider data is nil")
	}

	switch p.ProviderType {
	case store.ProviderCloudflareClef:
		accountID := store.ResolveClefAccountID(p)
		if accountID == "" {
			return nil, fmt.Errorf("clef: missing account_id in settings or api_base for %q", p.Name)
		}
		model := ""
		if s := store.ParseClefProviderSettings(p.Settings); s != nil {
			model = s.Model
		}
		clefProv := providers.NewCloudflareClefProvider(accountID, p.APIKey, model).WithName(p.Name)
		if strings.HasPrefix(p.APIBase, "http://") || strings.HasPrefix(p.APIBase, "https://") {
			base := p.APIBase
			if idx := strings.Index(base, "/accounts/"); idx != -1 {
				base = base[:idx+len("/accounts")]
			}
			clefProv.WithBaseURL(base)
		}
		return clefProv, nil

	case store.ProviderTypeSafeJev:
		jevProv := providers.NewTypeSafeAIJevProvider(p.APIKey, p.APIBase, "").WithName(p.Name)
		return jevProv, nil

	case store.ProviderOpenAIDecision:
		base := p.APIBase
		if idx := strings.Index(strings.ToLower(base), "/decisions"); idx != -1 {
			base = base[:idx]
		}
		decProv := providers.NewOpenAIDecisionProvider(p.APIKey, base, "").WithName(p.Name)
		return decProv, nil

	default:
		return nil, nil
	}
}

// RegisterDecisionProvider builds and registers a DecisionProvider in registry for the provider's tenant.
// Returns (nil, nil) if p is not a decision provider type.
func RegisterDecisionProvider(registry *providers.Registry, p *store.LLMProviderData) (providers.DecisionProvider, error) {
	if registry == nil || p == nil {
		return nil, nil
	}
	dp, err := BuildDecisionProvider(p)
	if err != nil {
		return nil, err
	}
	if dp == nil {
		return nil, nil
	}
	registry.RegisterDecisionProviderForTenant(p.TenantID, dp)
	return dp, nil
}

// ResolveDecisionProvider resolves the selected DecisionProvider for the tenant.
// It strictly reads "decision.provider" from system_configs for tenantID.
// If the key is empty or not found, it returns nil (meaning fallback to chat).
// If the selected provider is not found or not enabled, it logs a warning and returns nil.
func ResolveDecisionProvider(
	ctx context.Context,
	tenantID uuid.UUID,
	registry *providers.Registry,
	systemConfigs store.SystemConfigStore,
) providers.DecisionProvider {
	if registry == nil || systemConfigs == nil || tenantID == uuid.Nil {
		return nil
	}

	tctx := store.WithTenantID(ctx, tenantID)
	selectedName, err := systemConfigs.Get(tctx, DecisionProviderConfigKey)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			// Normal unconfigured state for this tenant
			slog.Debug("decision: provider not configured for tenant", "tenant_id", tenantID)
		} else {
			// Database or connection error reading config — log as warning
			slog.Warn("decision: failed to read system config", "tenant_id", tenantID, "key", DecisionProviderConfigKey, "error", err)
		}
		return nil
	}
	selectedName = strings.TrimSpace(selectedName)
	if selectedName == "" {
		slog.Debug("decision: provider explicitly empty for tenant", "tenant_id", tenantID)
		return nil
	}

	dp, err := registry.GetDecisionProviderForTenant(tenantID, selectedName)
	if err != nil || dp == nil {
		slog.Warn("decision: selected provider not found in registry",
			"tenant_id", tenantID, "provider", selectedName, "error", err)
		return nil
	}
	slog.Info("decision: provider resolved",
		"tenant_id", tenantID, "provider", dp.Name())
	return dp
}

// ClassifyDecisionError categorizes a decision error into a standardized reason and sanitized message.
// It explicitly strips any prompts, API keys, or raw upstream response bodies to prevent log leaks.
func ClassifyDecisionError(err error) (reason string, cleanMsg string) {
	if err == nil {
		return "ok", ""
	}
	errStr := err.Error()
	if strings.Contains(errStr, "deadline exceeded") {
		return "timeout", "deadline exceeded"
	}
	if strings.Contains(errStr, "low confidence") {
		return "low_confidence", "confidence below threshold"
	}
	if strings.Contains(errStr, "missing choice") || strings.Contains(errStr, "nil response") ||
		strings.Contains(errStr, "parse json") || strings.Contains(errStr, "parse response") ||
		strings.Contains(errStr, "invalid type") || strings.Contains(errStr, "partial results") {
		return "invalid_response", "malformed or missing answer schema"
	}
	if strings.Contains(errStr, "http error") {
		// Extract status code (e.g. "http error 429" or "http error 500") without logging raw response body
		parts := strings.Split(errStr, ":")
		for _, p := range parts {
			trimmed := strings.TrimSpace(p)
			if strings.HasPrefix(trimmed, "http error") {
				fields := strings.Fields(trimmed)
				if len(fields) >= 3 {
					return "upstream_http_error", fields[0] + " " + fields[1] + " " + fields[2]
				}
				return "upstream_http_error", trimmed
			}
		}
		return "upstream_http_error", "upstream returned non-200 status"
	}
	if strings.Contains(errStr, "http request") || strings.Contains(errStr, "connect:") || strings.Contains(errStr, "dial") {
		return "network_error", "connection or network failure"
	}
	return "provider_error", "decision evaluation failed"
}

