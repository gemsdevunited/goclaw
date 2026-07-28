package tools

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

type approvalResult struct {
	decision ApprovalDecision
	err      error
}

func approvalContext(tenantID uuid.UUID, userID string, agentID uuid.UUID) context.Context {
	ctx := store.WithTenantID(context.Background(), tenantID)
	ctx = store.WithUserID(ctx, userID)
	return store.WithAgentID(ctx, agentID)
}

func startApproval(t *testing.T, manager *ExecApprovalManager, ctx context.Context) (string, <-chan approvalResult) {
	t.Helper()
	result := make(chan approvalResult, 1)
	go func() {
		decision, err := manager.RequestApproval(ctx, "curl https://example.com", "default", time.Second)
		result <- approvalResult{decision: decision, err: err}
	}()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, pending := range manager.ListPendingForTenant(store.TenantIDFromContext(ctx)) {
			if pending.UserID == store.UserIDFromContext(ctx) {
				return pending.ID, result
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("approval was not added to pending queue")
	return "", nil
}

func awaitApproval(t *testing.T, result <-chan approvalResult) approvalResult {
	t.Helper()
	select {
	case got := <-result:
		return got
	case <-time.After(time.Second):
		t.Fatal("approval request did not return")
		return approvalResult{}
	}
}

func TestExecApprovalManager_RejectsMissingScope(t *testing.T) {
	manager := NewExecApprovalManager(DefaultExecApprovalConfig())
	ctx := store.WithTenantID(context.Background(), uuid.New())

	decision, err := manager.RequestApproval(ctx, "curl https://example.com", "default", time.Millisecond)
	if !errors.Is(err, ErrApprovalScope) {
		t.Fatalf("RequestApproval error = %v, want ErrApprovalScope", err)
	}
	if decision != ApprovalDeny {
		t.Fatalf("decision = %q, want deny", decision)
	}
	if got := manager.ListPendingForTenant(store.TenantIDFromContext(ctx)); len(got) != 0 {
		t.Fatalf("pending = %d, want 0", len(got))
	}
}

func TestExecApprovalManager_IsolatesPendingByTenant(t *testing.T) {
	manager := NewExecApprovalManager(DefaultExecApprovalConfig())
	tenantA, tenantB := uuid.New(), uuid.New()
	ctxA := approvalContext(tenantA, "user-a", uuid.New())
	id, result := startApproval(t, manager, ctxA)

	if got := manager.ListPendingForTenant(tenantB); len(got) != 0 {
		t.Fatalf("tenant B pending = %d, want 0", len(got))
	}
	if err := manager.ResolveForTenant(tenantB, id, ApprovalAllowOnce); !errors.Is(err, ErrApprovalNotFound) {
		t.Fatalf("cross-tenant resolve error = %v, want ErrApprovalNotFound", err)
	}
	if got := manager.ListPendingForTenant(tenantA); len(got) != 1 {
		t.Fatalf("tenant A pending = %d, want 1", len(got))
	}
	if err := manager.ResolveForTenant(tenantA, id, ApprovalAllowOnce); err != nil {
		t.Fatalf("same-tenant resolve: %v", err)
	}
	if got := awaitApproval(t, result); got.err != nil || got.decision != ApprovalAllowOnce {
		t.Fatalf("result = %+v, want allow-once", got)
	}
}

func TestExecApprovalManager_ResolvesOnlyOnce(t *testing.T) {
	mgr := NewExecApprovalManager(ExecApprovalConfig{})
	ctx := approvalContext(uuid.New(), "user-a", uuid.New())
	id, result := startApproval(t, mgr, ctx)

	if err := mgr.ResolveForTenant(store.TenantIDFromContext(ctx), id, ApprovalAllowOnce); err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	if err := mgr.ResolveForTenant(store.TenantIDFromContext(ctx), id, ApprovalDeny); !errors.Is(err, ErrApprovalNotFound) {
		t.Fatalf("second resolve error = %v, want ErrApprovalNotFound", err)
	}

	if got := awaitApproval(t, result); got.decision != ApprovalAllowOnce || got.err != nil {
		t.Fatalf("approval result = (%q, %v), want (%q, nil)", got.decision, got.err, ApprovalAllowOnce)
	}
}

func TestExecApprovalManager_AllowAlwaysIsScopedToRequester(t *testing.T) {
	manager := NewExecApprovalManager(ExecApprovalConfig{
		Security: ExecSecurityFull,
		Ask:      ExecAskOnMiss,
	})
	tenantA, tenantB := uuid.New(), uuid.New()
	agentA, agentB := uuid.New(), uuid.New()
	ctxA := approvalContext(tenantA, "user-a", agentA)
	id, result := startApproval(t, manager, ctxA)
	if err := manager.ResolveForTenant(tenantA, id, ApprovalAllowAlways); err != nil {
		t.Fatalf("resolve allow-always: %v", err)
	}
	if got := awaitApproval(t, result); got.err != nil || got.decision != ApprovalAllowAlways {
		t.Fatalf("result = %+v, want allow-always", got)
	}

	if got := manager.CheckCommand(ctxA, "curl https://example.com", "default"); got != "allow" {
		t.Fatalf("requester CheckCommand = %q, want allow", got)
	}
	for name, ctx := range map[string]context.Context{
		"other tenant": approvalContext(tenantB, "user-a", agentA),
		"other user":   approvalContext(tenantA, "user-b", agentA),
		"other agent":  approvalContext(tenantA, "user-a", agentB),
	} {
		if got := manager.CheckCommand(ctx, "curl https://example.com", "default"); got != "ask" {
			t.Errorf("%s CheckCommand = %q, want ask", name, got)
		}
	}
}
