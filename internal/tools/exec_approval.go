package tools

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// ExecSecurity determines the overall security mode for command execution.
type ExecSecurity string

const (
	// ExecSecurityDeny blocks all commands (no exec tool available).
	ExecSecurityDeny ExecSecurity = "deny"

	// ExecSecurityAllowlist only allows commands matching the allowlist.
	ExecSecurityAllowlist ExecSecurity = "allowlist"

	// ExecSecurityFull allows all commands (ask mode still applies).
	ExecSecurityFull ExecSecurity = "full"
)

// ExecAskMode determines when to prompt for user approval.
type ExecAskMode string

const (
	// ExecAskOff never asks — commands are auto-approved.
	ExecAskOff ExecAskMode = "off"

	// ExecAskOnMiss asks only when a command is not in the allowlist.
	ExecAskOnMiss ExecAskMode = "on-miss"

	// ExecAskAlways asks for every command execution.
	ExecAskAlways ExecAskMode = "always"
)

// ExecApprovalConfig configures command execution approval.
type ExecApprovalConfig struct {
	Security  ExecSecurity `json:"security"`  // "deny", "allowlist", "full" (default "full")
	Ask       ExecAskMode  `json:"ask"`       // "off", "on-miss", "always" (default "off")
	Allowlist []string     `json:"allowlist"` // glob patterns for allowed commands
}

// DefaultExecApprovalConfig returns the default (permissive) config.
func DefaultExecApprovalConfig() ExecApprovalConfig {
	return ExecApprovalConfig{
		Security: ExecSecurityFull,
		Ask:      ExecAskOff,
	}
}

// safeBins are command names that are always considered safe.
// Only includes read-only, text processing, and dev tools.
// Infrastructure/network tools (docker, kubectl, terraform, ansible,
// curl, wget, ssh, scp, rsync) are excluded — they require approval
// when ask mode is "on-miss".
var safeBins = map[string]bool{
	// Read-only / info tools
	"cat": true, "echo": true, "ls": true, "pwd": true, "head": true,
	"tail": true, "wc": true, "sort": true, "uniq": true, "grep": true,
	"find": true, "which": true, "whoami": true, "date": true,
	"uname": true, "hostname": true,
	"df": true, "du": true, "free": true, "uptime": true, "file": true,
	"stat": true, "dirname": true, "basename": true, "realpath": true,
	// Text processing
	"jq": true, "yq": true, "sed": true, "awk": true, "tr": true,
	"cut": true, "diff": true, "patch": true, "tee": true, "xargs": true,
	// Dev tools (core purpose of a coding agent)
	"git": true, "node": true, "npm": true, "npx": true, "yarn": true,
	"pnpm": true, "bun": true, "deno": true, "python": true, "python3": true,
	"pip": true, "pip3": true, "go": true, "cargo": true, "rustc": true,
	"make": true, "cmake": true, "gcc": true, "g++": true, "clang": true,
	"java": true, "javac": true, "mvn": true, "gradle": true,
}

// ApprovalDecision is the user's response to an approval request.
type ApprovalDecision string

const (
	ApprovalAllowOnce   ApprovalDecision = "allow-once"
	ApprovalAllowAlways ApprovalDecision = "allow-always"
	ApprovalDeny        ApprovalDecision = "deny"
)

// PendingApproval is an in-flight approval request.
type PendingApproval struct {
	ID        string    `json:"id"`
	Command   string    `json:"command"`
	AgentID   string    `json:"agentId"`
	CreatedAt time.Time `json:"createdAt"`
	TenantID  uuid.UUID `json:"-"`
	UserID    string    `json:"-"`
	resultCh  chan ApprovalDecision
}

var (
	// ErrApprovalNotFound intentionally covers missing, expired, and out-of-scope
	// approvals so callers cannot enumerate another tenant's pending requests.
	ErrApprovalNotFound = errors.New("approval not found")
	ErrApprovalScope    = errors.New("exec approval requires tenant and user context")
)

type approvalAllowKey struct {
	tenantID uuid.UUID
	userID   string
	agentID  string
	binary   string
}

// ExecApprovalManager manages pending approval requests and the dynamic allowlist.
type ExecApprovalManager struct {
	config      ExecApprovalConfig
	pending     map[string]*PendingApproval
	alwaysAllow map[approvalAllowKey]bool // requester-scoped "allow-always" decisions
	mu          sync.Mutex
	nextID      int
}

// NewExecApprovalManager creates an approval manager with the given config.
func NewExecApprovalManager(cfg ExecApprovalConfig) *ExecApprovalManager {
	return &ExecApprovalManager{
		config:      cfg,
		pending:     make(map[string]*PendingApproval),
		alwaysAllow: make(map[approvalAllowKey]bool),
	}
}

// CheckCommand evaluates whether a command should be executed, blocked, or needs approval.
// Returns: "allow", "deny", or "ask".
func (m *ExecApprovalManager) CheckCommand(ctx context.Context, command, fallbackAgentID string) string {
	switch m.config.Security {
	case ExecSecurityDeny:
		return "deny"

	case ExecSecurityAllowlist:
		if m.matchesAllowlist(ctx, command, fallbackAgentID) {
			if m.config.Ask == ExecAskAlways {
				return "ask"
			}
			return "allow"
		}
		if m.config.Ask == ExecAskOff {
			return "deny" // not in allowlist, no asking
		}
		return "ask"

	case ExecSecurityFull:
		switch m.config.Ask {
		case ExecAskOff:
			return "allow"
		case ExecAskAlways:
			return "ask"
		case ExecAskOnMiss:
			if m.matchesAllowlist(ctx, command, fallbackAgentID) || m.isSafeBin(command) {
				return "allow"
			}
			return "ask"
		}
	}

	return "allow"
}

// RequestApproval creates a pending approval and blocks until resolved or timeout.
func (m *ExecApprovalManager) RequestApproval(ctx context.Context, command, fallbackAgentID string, timeout time.Duration) (ApprovalDecision, error) {
	scope, err := approvalScopeFromContext(ctx, fallbackAgentID)
	if err != nil {
		return ApprovalDeny, err
	}

	m.mu.Lock()
	m.nextID++
	id := fmt.Sprintf("exec-%d", m.nextID)
	pa := &PendingApproval{
		ID:        id,
		Command:   command,
		AgentID:   scope.agentID,
		CreatedAt: time.Now(),
		TenantID:  scope.tenantID,
		UserID:    scope.userID,
		resultCh:  make(chan ApprovalDecision, 1),
	}
	m.pending[id] = pa
	m.mu.Unlock()

	slog.Info("exec approval requested", "id", id, "command", truncateCmd(command, 100))

	// Wait for resolution or timeout
	select {
	case decision := <-pa.resultCh:
		m.mu.Lock()
		delete(m.pending, id)
		m.mu.Unlock()

		// If allow-always, add the command's base binary to the dynamic allowlist
		if decision == ApprovalAllowAlways {
			bin := extractBin(command)
			if bin != "" {
				m.mu.Lock()
				m.alwaysAllow[approvalAllowKey{
					tenantID: scope.tenantID,
					userID:   scope.userID,
					agentID:  scope.agentID,
					binary:   bin,
				}] = true
				m.mu.Unlock()
				slog.Info("exec approval: added to always-allow", "bin", bin)
			}
		}

		return decision, nil

	case <-time.After(timeout):
		m.mu.Lock()
		delete(m.pending, id)
		m.mu.Unlock()
		return ApprovalDeny, fmt.Errorf("approval timed out after %s", timeout)
	}
}

// ResolveForTenant resolves a pending approval request in tenantID.
func (m *ExecApprovalManager) ResolveForTenant(tenantID uuid.UUID, id string, decision ApprovalDecision) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	pa, ok := m.pending[id]
	if !ok || tenantID == uuid.Nil || pa.TenantID != tenantID {
		return ErrApprovalNotFound
	}

	// Remove it before notifying the requester so a concurrent resolver cannot
	// report a second successful decision for the same approval.
	delete(m.pending, id)
	pa.resultCh <- decision
	return nil
}

// ListPendingForTenant returns only pending approvals owned by tenantID.
func (m *ExecApprovalManager) ListPendingForTenant(tenantID uuid.UUID) []*PendingApproval {
	if tenantID == uuid.Nil {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	result := make([]*PendingApproval, 0, len(m.pending))
	for _, pa := range m.pending {
		if pa.TenantID == tenantID {
			result = append(result, pa)
		}
	}
	return result
}

// matchesAllowlist checks if a command matches any allowlist pattern or dynamic always-allow.
func (m *ExecApprovalManager) matchesAllowlist(ctx context.Context, command, fallbackAgentID string) bool {
	bin := extractBin(command)

	// Check dynamic always-allow
	if scope, err := approvalScopeFromContext(ctx, fallbackAgentID); err == nil {
		m.mu.Lock()
		allowed := m.alwaysAllow[approvalAllowKey{
			tenantID: scope.tenantID,
			userID:   scope.userID,
			agentID:  scope.agentID,
			binary:   bin,
		}]
		m.mu.Unlock()
		if allowed {
			return true
		}
	}

	// Check static allowlist patterns
	for _, pattern := range m.config.Allowlist {
		if matched, _ := filepath.Match(pattern, bin); matched {
			return true
		}
		// Also match against full command
		if matched, _ := filepath.Match(pattern, command); matched {
			return true
		}
	}

	return false
}

type approvalScope struct {
	tenantID uuid.UUID
	userID   string
	agentID  string
}

func approvalScopeFromContext(ctx context.Context, fallbackAgentID string) (approvalScope, error) {
	tenantID := store.TenantIDFromContext(ctx)
	userID := store.UserIDFromContext(ctx)
	if tenantID == uuid.Nil || userID == "" {
		return approvalScope{}, ErrApprovalScope
	}

	agentID := fallbackAgentID
	if id := store.AgentIDFromContext(ctx); id != uuid.Nil {
		agentID = id.String()
	}
	if agentID == "" {
		return approvalScope{}, ErrApprovalScope
	}

	return approvalScope{tenantID: tenantID, userID: userID, agentID: agentID}, nil
}

// isSafeBin checks if the command's base binary is in the safe list.
func (m *ExecApprovalManager) isSafeBin(command string) bool {
	return safeBins[extractBin(command)]
}

// extractBin returns the first word of a command (the binary name).
func extractBin(command string) string {
	command = strings.TrimSpace(command)
	// Skip env var assignments like FOO=bar cmd
	for strings.Contains(command, "=") {
		parts := strings.SplitN(command, " ", 2)
		if !strings.Contains(parts[0], "=") {
			break
		}
		if len(parts) < 2 {
			return ""
		}
		command = strings.TrimSpace(parts[1])
	}

	fields := strings.Fields(command)
	if len(fields) == 0 {
		return ""
	}
	return filepath.Base(fields[0])
}

func truncateCmd(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
