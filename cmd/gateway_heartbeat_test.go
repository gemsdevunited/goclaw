package cmd

import (
	"testing"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

type cronEventStoreStub struct {
	store.CronStore
	onEvent func(store.CronEvent)
}

func (s *cronEventStoreStub) SetOnEvent(fn func(store.CronEvent)) {
	s.onEvent = fn
}

type cronEventPublisherSpy struct {
	events []bus.Event
}

func (s *cronEventPublisherSpy) Subscribe(string, bus.EventHandler) {}
func (s *cronEventPublisherSpy) Unsubscribe(string)                 {}
func (s *cronEventPublisherSpy) Broadcast(event bus.Event) {
	s.events = append(s.events, event)
}

func TestWireCronEventsPublishesTenantScopedUserEvent(t *testing.T) {
	cronStore := &cronEventStoreStub{}
	publisher := &cronEventPublisherSpy{}
	tenantID := uuid.New()

	wireCronEvents(cronStore, publisher)
	if cronStore.onEvent == nil {
		t.Fatal("cron event callback was not registered")
	}

	want := store.CronEvent{
		Action:   "completed",
		JobID:    "job-1",
		UserID:   "user-a",
		TenantID: tenantID,
	}
	cronStore.onEvent(want)

	if len(publisher.events) != 1 {
		t.Fatalf("published events = %d, want 1", len(publisher.events))
	}
	got := publisher.events[0]
	if got.Name != protocol.EventCron {
		t.Errorf("event name = %q, want %q", got.Name, protocol.EventCron)
	}
	if got.TenantID != tenantID {
		t.Errorf("tenant ID = %s, want %s", got.TenantID, tenantID)
	}
	payload, ok := got.Payload.(store.CronEvent)
	if !ok {
		t.Fatalf("payload type = %T, want store.CronEvent", got.Payload)
	}
	if payload != want {
		t.Errorf("payload = %+v, want %+v", payload, want)
	}
}
