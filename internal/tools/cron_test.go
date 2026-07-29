package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/gemsterinbox"
	"github.com/nextlevelbuilder/goclaw/internal/outbounddelivery"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// cronToolWithInboxDestination mirrors the production wiring: a CronTool
// whose destinations set contains the Gemster Inbox route.
func cronToolWithInboxDestination(cronStore *testCronStore) *CronTool {
	tool := NewCronTool(cronStore)
	tool.SetDestinations(outbounddelivery.NewDestinationSet(
		gemsterinbox.NewDestination(nopSender{}),
	))
	return tool
}

type nopSender struct{}

func (nopSender) Send(_ context.Context, _ outbounddelivery.Delivery) error { return nil }

type testCronStore struct {
	jobs      map[string]*store.CronJob
	addCnt    int
	lastAdded *store.CronJob
	updateCnt int
	runCnt    int
	lastForce bool
	updateErr error
	runErr    error
}

func newTestCronStore(job *store.CronJob) *testCronStore {
	jobs := map[string]*store.CronJob{}
	if job != nil {
		jobs[job.ID] = job
	}
	return &testCronStore{jobs: jobs}
}

func (s *testCronStore) AddJob(_ context.Context, name string, schedule store.CronSchedule, message string, deliver bool, channel, to, agentID, userID string) (*store.CronJob, error) {
	s.addCnt++
	job := &store.CronJob{
		ID:             "new-job",
		Name:           name,
		Schedule:       schedule,
		Payload:        store.CronPayload{Message: message},
		Deliver:        deliver,
		DeliverChannel: channel,
		DeliverTo:      to,
		AgentID:        agentID,
		UserID:         userID,
	}
	s.jobs[job.ID] = job
	s.lastAdded = job
	return job, nil
}

func (s *testCronStore) GetJob(_ context.Context, jobID string) (*store.CronJob, bool) {
	job, ok := s.jobs[jobID]
	return job, ok
}

func (s *testCronStore) ListJobs(context.Context, bool, string, string) []store.CronJob {
	var jobs []store.CronJob
	for _, job := range s.jobs {
		jobs = append(jobs, *job)
	}
	return jobs
}

func (s *testCronStore) RemoveJob(context.Context, string) error { return nil }

func (s *testCronStore) UpdateJob(_ context.Context, jobID string, patch store.CronJobPatch) (*store.CronJob, error) {
	s.updateCnt++
	if s.updateErr != nil {
		return nil, s.updateErr
	}
	job := s.jobs[jobID]
	if patch.Message != "" {
		job.Payload.Message = patch.Message
	}
	return job, nil
}

func (s *testCronStore) EnableJob(context.Context, string, bool) error { return nil }
func (s *testCronStore) GetRunLog(context.Context, string, int, int) ([]store.CronRunLogEntry, int) {
	return nil, 0
}
func (s *testCronStore) Status() map[string]any                                      { return map[string]any{} }
func (s *testCronStore) Start() error                                                { return nil }
func (s *testCronStore) Stop()                                                       {}
func (s *testCronStore) SetOnJob(func(*store.CronJob) (*store.CronJobResult, error)) {}
func (s *testCronStore) SetOnEvent(func(store.CronEvent))                            {}

func (s *testCronStore) RunJob(_ context.Context, _ string, force bool) (bool, string, error) {
	s.runCnt++
	s.lastForce = force
	return true, "", s.runErr
}

func (s *testCronStore) GetDueJobs(time.Time) []store.CronJob { return nil }
func (s *testCronStore) SetDefaultTimezone(string)            {}

func TestCronToolValidatesGemsterInboxBeforeCreatingJob(t *testing.T) {
	cronStore := newTestCronStore(nil)
	tool := cronToolWithInboxDestination(cronStore)
	ctx := store.WithUserID(context.Background(), "not-an-email")

	result := tool.Execute(ctx, map[string]any{
		"action": "add",
		"job": map[string]any{
			"name":    "report",
			"message": "send report",
			"deliver": true,
			"channel": gemsterinbox.Destination,
			"schedule": map[string]any{
				"kind":    "every",
				"everyMs": float64(60_000),
			},
		},
	})

	if !result.IsError {
		t.Fatalf("expected invalid recipient error, got %#v", result)
	}
	if cronStore.addCnt != 0 {
		t.Fatalf("AddJob called %d times; invalid Gemster Inbox job must not be persisted", cronStore.addCnt)
	}
}

func TestCronToolKeepsExplicitGemsterInboxDestination(t *testing.T) {
	cronStore := newTestCronStore(nil)
	tool := cronToolWithInboxDestination(cronStore)
	ctx := store.WithUserID(context.Background(), "user@example.com")
	ctx = WithToolChannel(ctx, "ws")
	ctx = WithToolChatID(ctx, "socket-1")

	result := tool.Execute(ctx, map[string]any{
		"action": "add",
		"job": map[string]any{
			"name":    "report",
			"message": "send report",
			"deliver": true,
			"channel": gemsterinbox.Destination,
			"schedule": map[string]any{
				"kind":    "every",
				"everyMs": float64(60_000),
			},
		},
	})

	if result.IsError {
		t.Fatalf("add returned error: %#v", result)
	}
	if cronStore.lastAdded == nil || cronStore.lastAdded.DeliverChannel != gemsterinbox.Destination {
		t.Fatalf("explicit Gemster Inbox destination was overwritten: %#v", cronStore.lastAdded)
	}
	if cronStore.lastAdded.DeliverTo != "user@example.com" {
		t.Fatalf("recipient = %q, want user email", cronStore.lastAdded.DeliverTo)
	}
}

func TestCronToolRejectsGemsterInboxOnGroupUpdate(t *testing.T) {
	const groupUserID = "group:telegram:-100123"
	cronStore := newTestCronStore(&store.CronJob{ID: "job-1", UserID: groupUserID})
	tool := cronToolWithInboxDestination(cronStore)
	ctx := store.WithUserID(context.Background(), groupUserID)
	ctx = WithToolPeerKind(ctx, "group")
	deliver := true

	result := tool.Execute(ctx, map[string]any{
		"action": "update",
		"jobId":  "job-1",
		"patch": map[string]any{
			"deliver":        deliver,
			"deliverChannel": gemsterinbox.Destination,
		},
	})

	if !result.IsError {
		t.Fatalf("expected direct-user validation error, got %#v", result)
	}
	if cronStore.updateCnt != 0 {
		t.Fatalf("UpdateJob called %d times for invalid group target", cronStore.updateCnt)
	}
}

func TestCronToolBlocksCredentialBoundUpdateByDifferentUser(t *testing.T) {
	cronStore := newTestCronStore(&store.CronJob{
		ID:     "job-1",
		UserID: "group:telegram:-100123",
		Payload: store.CronPayload{
			Message:          "old",
			CredentialUserID: "tenant-user-a",
		},
	})
	tool := NewCronTool(cronStore)
	ctx := store.WithUserID(context.Background(), "group:telegram:-100123")
	ctx = store.WithCredentialUserID(ctx, "tenant-user-b")

	result := tool.Execute(ctx, map[string]any{
		"action": "update",
		"jobId":  "job-1",
		"patch":  map[string]any{"message": "run gh issue list"},
	})

	if !result.IsError || !strings.Contains(result.ForLLM, "credential context") {
		t.Fatalf("expected credential context error, got %#v", result)
	}
	if cronStore.updateCnt != 0 {
		t.Fatalf("UpdateJob called %d times, want 0", cronStore.updateCnt)
	}
}

func TestCronToolListRedactsCredentialUserID(t *testing.T) {
	cronStore := newTestCronStore(&store.CronJob{
		ID:     "job-1",
		UserID: "group:telegram:-100123",
		Payload: store.CronPayload{
			Message:          "run gh issue list",
			CredentialUserID: "tenant-user-a",
		},
	})
	tool := NewCronTool(cronStore)
	ctx := store.WithUserID(context.Background(), "group:telegram:-100123")

	result := tool.Execute(ctx, map[string]any{"action": "list", "includeDisabled": true})

	if result.IsError {
		t.Fatalf("list returned error: %#v", result)
	}
	if strings.Contains(result.ForLLM, "tenant-user-a") || strings.Contains(result.ForLLM, "credentialUserId") {
		t.Fatalf("credential identity leaked in list response: %s", result.ForLLM)
	}
}

func TestCronToolBlocksCredentialBoundRunByDifferentUser(t *testing.T) {
	cronStore := newTestCronStore(&store.CronJob{
		ID:     "job-1",
		UserID: "group:telegram:-100123",
		Payload: store.CronPayload{
			Message:          "run gh issue list",
			CredentialUserID: "tenant-user-a",
		},
	})
	tool := NewCronTool(cronStore)
	ctx := store.WithUserID(context.Background(), "group:telegram:-100123")
	ctx = store.WithCredentialUserID(ctx, "tenant-user-b")

	result := tool.Execute(ctx, map[string]any{
		"action":  "run",
		"jobId":   "job-1",
		"runMode": "force",
	})

	if !result.IsError || !strings.Contains(result.ForLLM, "credential context") {
		t.Fatalf("expected credential context error, got %#v", result)
	}
	if cronStore.runCnt != 0 {
		t.Fatalf("RunJob called %d times, want 0", cronStore.runCnt)
	}
}

// A command payload must not be slipped in via update when command cron is
// disabled — the disabled-gateway contract is that command jobs cannot be
// created OR mutated into existence.
func TestCronToolUpdateBlocksCommandWhenDisabled(t *testing.T) {
	cronStore := newTestCronStore(&store.CronJob{ID: "job-1", Payload: store.CronPayload{Message: "old"}})
	tool := NewCronTool(cronStore) // commandEnabled defaults to false

	result := tool.Execute(context.Background(), map[string]any{
		"action": "update",
		"jobId":  "job-1",
		"patch":  map[string]any{"commandArgv": []any{"echo", "hi"}},
	})

	if !result.IsError || !strings.Contains(result.ForLLM, "disabled") {
		t.Fatalf("expected command-disabled error, got %#v", result)
	}
	if cronStore.updateCnt != 0 {
		t.Fatalf("UpdateJob called %d times, want 0", cronStore.updateCnt)
	}
}

// Update must validate the command spec; an invalid argv must be rejected even
// when command cron is enabled.
func TestCronToolUpdateRejectsInvalidCommandSpec(t *testing.T) {
	cronStore := newTestCronStore(&store.CronJob{ID: "job-1", Payload: store.CronPayload{Message: "old"}})
	tool := NewCronTool(cronStore)
	tool.SetCommandEnabled(true)

	result := tool.Execute(context.Background(), map[string]any{
		"action": "update",
		"jobId":  "job-1",
		"patch":  map[string]any{"commandArgv": []any{""}}, // argv[0] empty → invalid
	})

	if !result.IsError {
		t.Fatalf("expected invalid command spec error, got %#v", result)
	}
	if cronStore.updateCnt != 0 {
		t.Fatalf("UpdateJob called %d times, want 0", cronStore.updateCnt)
	}
}
