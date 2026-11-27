package store

import "context"

// SystemConfigStore manages per-tenant configuration settings.
// Non-secret, plain-text key-value pairs. Use ConfigSecretsStore for secrets.
//
// Resolution: strictly tenant-specific based on tenant in context.
// All entries are stored per tenant_id. No sentinel UUID.
type SystemConfigStore interface {
	// Get returns the config value for the tenant in context.
	Get(ctx context.Context, key string) (string, error)
	// Set stores a config value for the current tenant (from context).
	Set(ctx context.Context, key, value string) error
	// Delete removes a config value for the current tenant.
	Delete(ctx context.Context, key string) error
	// List returns all configs for the current tenant.
	List(ctx context.Context) (map[string]string, error)
}
