package methods

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/gateway"
	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

type methodApprovalResult struct {
	decision tools.ApprovalDecision
	err      error
}

func methodApprovalContext(tenantID uuid.UUID, userID string) context.Context {
	ctx := store.WithTenantID(context.Background(), tenantID)
	ctx = store.WithUserID(ctx, userID)
	return store.WithAgentID(ctx, uuid.New())
}

func startMethodApproval(t *testing.T, manager *tools.ExecApprovalManager, ctx context.Context) (string, <-chan methodApprovalResult) {
	t.Helper()
	result := make(chan methodApprovalResult, 1)
	go func() {
		decision, err := manager.RequestApproval(ctx, "curl https://example.com", "default", time.Second)
		result <- methodApprovalResult{decision: decision, err: err}
	}()

	tenantID := store.TenantIDFromContext(ctx)
	userID := store.UserIDFromContext(ctx)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, pending := range manager.ListPendingForTenant(tenantID) {
			if pending.UserID == userID {
				return pending.ID, result
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("approval was not added to pending queue")
	return "", nil
}

func readApprovalResponse(t *testing.T, responses <-chan []byte) protocol.ResponseFrame {
	t.Helper()
	select {
	case raw := <-responses:
		var response protocol.ResponseFrame
		if err := json.Unmarshal(raw, &response); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		return response
	case <-time.After(time.Second):
		t.Fatal("handler did not respond")
		return protocol.ResponseFrame{}
	}
}

func approvalReq(t *testing.T, method string, params map[string]any) *protocol.RequestFrame {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	return &protocol.RequestFrame{Type: protocol.FrameTypeRequest, ID: "approval-request", Method: method, Params: raw}
}

func TestExecApprovalMethods_ListScopesTenantAndUser(t *testing.T) {
	manager := tools.NewExecApprovalManager(tools.DefaultExecApprovalConfig())
	methods := NewExecApprovalMethods(manager, nil)
	tenantA, tenantB := uuid.New(), uuid.New()
	_, resultA := startMethodApproval(t, manager, methodApprovalContext(tenantA, "user-a"))
	_, resultOtherUser := startMethodApproval(t, manager, methodApprovalContext(tenantA, "user-b"))
	_, resultB := startMethodApproval(t, manager, methodApprovalContext(tenantB, "user-c"))

	operator, responses := gateway.NewCapturingTestClient(permissions.RoleOperator, tenantA, "user-a", 1)
	methods.handleList(context.Background(), operator, approvalReq(t, protocol.MethodApprovalsList, nil))
	response := readApprovalResponse(t, responses)
	items := response.Payload.(map[string]any)["pending"].([]any)
	if len(items) != 1 {
		t.Fatalf("operator pending = %d, want 1", len(items))
	}

	admin, adminResponses := gateway.NewCapturingTestClient(permissions.RoleAdmin, tenantA, "admin", 1)
	methods.handleList(context.Background(), admin, approvalReq(t, protocol.MethodApprovalsList, nil))
	response = readApprovalResponse(t, adminResponses)
	items = response.Payload.(map[string]any)["pending"].([]any)
	if len(items) != 2 {
		t.Fatalf("admin pending = %d, want 2", len(items))
	}

	for _, pending := range manager.ListPendingForTenant(tenantA) {
		if err := manager.ResolveForTenant(tenantA, pending.ID, tools.ApprovalDeny); err != nil {
			t.Fatalf("cleanup tenant A approval: %v", err)
		}
	}
	for _, pending := range manager.ListPendingForTenant(tenantB) {
		if err := manager.ResolveForTenant(tenantB, pending.ID, tools.ApprovalDeny); err != nil {
			t.Fatalf("cleanup tenant B approval: %v", err)
		}
	}
	for _, result := range []<-chan methodApprovalResult{resultA, resultOtherUser, resultB} {
		if got := <-result; got.err != nil || got.decision != tools.ApprovalDeny {
			t.Fatalf("cleanup result = %+v", got)
		}
	}
}

func TestExecApprovalMethods_ResolveRequiresOwnerOrTenantAdmin(t *testing.T) {
	manager := tools.NewExecApprovalManager(tools.DefaultExecApprovalConfig())
	methods := NewExecApprovalMethods(manager, nil)
	tenantA, tenantB := uuid.New(), uuid.New()
	id, result := startMethodApproval(t, manager, methodApprovalContext(tenantA, "user-a"))

	otherUser, otherResponses := gateway.NewCapturingTestClient(permissions.RoleOperator, tenantA, "user-b", 1)
	methods.handleApprove(context.Background(), otherUser, approvalReq(t, protocol.MethodApprovalsApprove, map[string]any{"id": id}))
	if response := readApprovalResponse(t, otherResponses); response.Error == nil || response.Error.Code != protocol.ErrNotFound {
		t.Fatalf("other-user response = %+v, want NOT_FOUND", response)
	}
	if got := manager.ListPendingForTenant(tenantA); len(got) != 1 {
		t.Fatalf("pending after rejected resolve = %d, want 1", len(got))
	}

	crossTenantAdmin, crossTenantResponses := gateway.NewCapturingTestClient(permissions.RoleAdmin, tenantB, "admin-b", 1)
	methods.handleApprove(context.Background(), crossTenantAdmin, approvalReq(t, protocol.MethodApprovalsApprove, map[string]any{"id": id}))
	if response := readApprovalResponse(t, crossTenantResponses); response.Error == nil || response.Error.Code != protocol.ErrNotFound {
		t.Fatalf("cross-tenant response = %+v, want NOT_FOUND", response)
	}

	admin, adminResponses := gateway.NewCapturingTestClient(permissions.RoleAdmin, tenantA, "admin-a", 1)
	methods.handleApprove(context.Background(), admin, approvalReq(t, protocol.MethodApprovalsApprove, map[string]any{"id": id, "always": true}))
	if response := readApprovalResponse(t, adminResponses); response.Error != nil {
		t.Fatalf("admin response = %+v", response.Error)
	}
	select {
	case got := <-result:
		if got.err != nil || got.decision != tools.ApprovalAllowAlways {
			t.Fatalf("approval result = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("approval request did not return")
	}
}
