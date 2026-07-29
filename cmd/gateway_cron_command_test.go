//go:build !windows

package cmd

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/config"
	"github.com/nextlevelbuilder/goclaw/internal/gemsterinbox"
	"github.com/nextlevelbuilder/goclaw/internal/outbounddelivery"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// recordingDestinationSet builds a DestinationSet with a single inbox route
// that records every delivery sent through it.
func recordingDestinationSet(sender *recordingInboxSender) outbounddelivery.DestinationSet {
	return outbounddelivery.NewDestinationSet(gemsterinbox.NewDestination(sender))
}

func commandCronConfig(enabled bool) *config.Config {
	c := &config.Config{}
	c.Cron.CommandEnabled = enabled
	return c
}

func commandCronJob(spec *store.CronCommandSpec, deliver bool) *store.CronJob {
	job := &store.CronJob{
		ID:       uuid.NewString(),
		TenantID: uuid.New(),
		Name:     "probe",
		AgentID:  "ops",
		UserID:   "user-1",
		Payload:  store.CronPayload{Kind: store.CronPayloadKindCommand, Command: spec},
	}
	if deliver {
		job.Deliver = true
		job.DeliverChannel = "telegram"
		job.DeliverTo = "chat-1"
	}
	return job
}

type recordingInboxSender struct {
	deliveries []outbounddelivery.Delivery
}

func (s *recordingInboxSender) Send(_ context.Context, delivery outbounddelivery.Delivery) error {
	s.deliveries = append(s.deliveries, delivery)
	return nil
}

// A command payload must be refused unless cron.command_enabled is set.
func TestCronJobHandler_CommandDisabled(t *testing.T) {
	handler := makeCronJobHandler(nil, nil, commandCronConfig(false), nil, nil, nil, nil, nil, nil, outbounddelivery.DestinationSet{})
	if _, err := handler(commandCronJob(&store.CronCommandSpec{Argv: []string{"sh", "-c", "echo hi"}}, false)); err == nil {
		t.Fatal("expected error when cron.command_enabled is false")
	}
}

// A successful command runs with zero model tokens and its stdout is delivered.
func TestCronJobHandler_CommandSuccessDelivers(t *testing.T) {
	mb := bus.New()
	defer mb.Close()

	handler := makeCronJobHandler(nil, mb, commandCronConfig(true), nil, nil, nil, nil, nil, nil, outbounddelivery.DestinationSet{})
	result, err := handler(commandCronJob(&store.CronCommandSpec{Argv: []string{"sh", "-c", "printf hello"}}, true))
	if err != nil {
		t.Fatalf("command cron returned error: %v", err)
	}
	if result == nil || result.Content != "hello" {
		t.Fatalf("result = %#v, want hello", result)
	}
	if result.InputTokens != 0 || result.OutputTokens != 0 {
		t.Errorf("command cron must report zero tokens, got in=%d out=%d", result.InputTokens, result.OutputTokens)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	got, ok := mb.SubscribeOutbound(ctx)
	if !ok {
		t.Fatal("expected outbound delivery of command output")
	}
	if got.Content != "hello" || got.Channel != "telegram" || got.ChatID != "chat-1" {
		t.Fatalf("outbound = %#v, want telegram/chat-1/hello", got)
	}
}

func TestCronJobHandler_CommandInboxUsesSharedSender(t *testing.T) {
	mb := bus.New()
	defer mb.Close()

	job := commandCronJob(&store.CronCommandSpec{Argv: []string{"sh", "-c", "printf hello"}}, true)
	job.UserID = "ops@example.com"
	job.DeliverChannel = "gemster_inbox"
	job.DeliverTo = "ops@example.com"
	executionID := uuid.New()
	job.ExecutionID = executionID
	sender := &recordingInboxSender{}
	handler := makeCronJobHandler(nil, mb, commandCronConfig(true), nil, nil, nil, nil, nil, nil, recordingDestinationSet(sender))

	result, err := handler(job)
	if err != nil {
		t.Fatalf("command inbox returned error: %v", err)
	}
	if result == nil || result.Content != "hello" {
		t.Fatalf("result = %#v, want hello", result)
	}
	if len(sender.deliveries) != 1 || sender.deliveries[0].ID != executionID.String() || sender.deliveries[0].Recipient != "ops@example.com" {
		t.Fatalf("deliveries = %#v, want one delivery for execution", sender.deliveries)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if got, ok := mb.SubscribeOutbound(ctx); ok {
		t.Fatalf("gemster_inbox must not publish to legacy bus, got %#v", got)
	}
}

func TestCronJobHandler_CommandInboxRequiresConfiguredSender(t *testing.T) {
	job := commandCronJob(&store.CronCommandSpec{Argv: []string{"sh", "-c", "exit 99"}}, true)
	job.UserID = "ops@example.com"
	job.DeliverChannel = gemsterinbox.Destination

	// Destination registered by name, but Sender is nil: the cron handler
	// must short-circuit with "<name> delivery is not configured" before
	// running the command.
	registeredButUnconfigured := outbounddelivery.NewDestinationSet(outbounddelivery.Destination{
		Name:   gemsterinbox.Destination,
		Sender: nil,
	})
	handler := makeCronJobHandler(nil, nil, commandCronConfig(true), nil, nil, nil, nil, nil, nil, registeredButUnconfigured)
	if _, err := handler(job); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("error = %v, want configuration error before command execution", err)
	}
}

func TestSendDestinationDeliverySuppressesNoReply(t *testing.T) {
	job := commandCronJob(&store.CronCommandSpec{Argv: []string{"sh", "-c", "true"}}, true)
	job.UserID = "ops@example.com"
	job.DeliverChannel = gemsterinbox.Destination

	sender := &recordingInboxSender{}
	err := sendDestinationDelivery(context.Background(), job, uuid.New(), "NO_REPLY", recordingDestinationSet(sender))
	if err != nil {
		t.Fatalf("sendDestinationDelivery returned error: %v", err)
	}
	if len(sender.deliveries) != 0 {
		t.Fatalf("NO_REPLY must not send an Inbox delivery, got %#v", sender.deliveries)
	}
}

// A non-zero exit returns an error and is not delivered.
func TestCronJobHandler_CommandFailureNotDelivered(t *testing.T) {
	mb := bus.New()
	defer mb.Close()

	handler := makeCronJobHandler(nil, mb, commandCronConfig(true), nil, nil, nil, nil, nil, nil, outbounddelivery.DestinationSet{})
	result, err := handler(commandCronJob(&store.CronCommandSpec{Argv: []string{"sh", "-c", "echo boom 1>&2; exit 3"}}, true))
	if err == nil {
		t.Fatal("expected error for non-zero command exit")
	}
	if result != nil {
		t.Fatalf("failed command should return nil result, got %#v", result)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if got, ok := mb.SubscribeOutbound(ctx); ok {
		t.Fatalf("failed command must not deliver, got %#v", got)
	}
}

// An empty argv is rejected before execution.
func TestCronJobHandler_CommandInvalidSpec(t *testing.T) {
	handler := makeCronJobHandler(nil, nil, commandCronConfig(true), nil, nil, nil, nil, nil, nil, outbounddelivery.DestinationSet{})
	if _, err := handler(commandCronJob(&store.CronCommandSpec{}, false)); err == nil {
		t.Fatal("expected error for empty argv")
	}
}
