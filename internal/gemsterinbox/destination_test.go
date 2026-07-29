package gemsterinbox

import (
	"context"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/outbounddelivery"
)

func TestNewDestination_WiresNameSenderAndResolver(t *testing.T) {
	d := NewDestination(&fakeInboxSender{})
	if d.Name != Destination {
		t.Fatalf("Name = %q, want %q", d.Name, Destination)
	}
	if d.Sender == nil {
		t.Fatal("Sender must be non-nil")
	}
	if d.Resolver == nil {
		t.Fatal("Resolver must be non-nil")
	}
}

func TestNewDestination_ResolverDelegatesToRecipientFor(t *testing.T) {
	d := NewDestination(&fakeInboxSender{})
	got, err := d.Resolver.ResolveRecipient("user@example.com", "direct")
	if err != nil {
		t.Fatalf("Resolver.ResolveRecipient = %v", err)
	}
	if got != "user@example.com" {
		t.Fatalf("Resolver returned %q, want %q", got, "user@example.com")
	}
}

func TestNewDestination_ResolverRejectsGroupAndInvalidEmail(t *testing.T) {
	d := NewDestination(&fakeInboxSender{})
	if _, err := d.Resolver.ResolveRecipient("user@example.com", "group"); err == nil {
		t.Fatal("group context must be rejected")
	}
	if _, err := d.Resolver.ResolveRecipient("not-an-email", "direct"); err == nil {
		t.Fatal("non-email user ID must be rejected")
	}
}

// NewDestination(nil) must still produce a destination that registers in a set.
// The composition root builds the set before knowing whether the env config
// resolves to a sender; if it doesn't, the cron handler must see the registered
// name and report "<name> delivery is not configured" instead of silently
// falling back to the legacy chat bus.
func TestNewDestination_NilSenderStaysRegisteredInSet(t *testing.T) {
	d := NewDestination(nil)
	if d.Name != Destination {
		t.Fatalf("Name = %q, want %q", d.Name, Destination)
	}
	if d.Sender != nil {
		t.Fatalf("Sender = %v, want nil to preserve configured-but-unconfigured state", d.Sender)
	}
	if d.Resolver == nil {
		t.Fatal("Resolver must remain wired even when Sender is nil")
	}

	set := outbounddelivery.NewDestinationSet(d)
	if !set.Has(Destination) {
		t.Fatalf("set must contain %q even when Sender is nil", Destination)
	}
	got, ok := set.Get(Destination)
	if !ok || got.Sender != nil {
		t.Fatalf("Get returned (%+v, %v); want registered entry with nil Sender", got, ok)
	}
}

// fakeInboxSender is the minimum outbounddelivery.Sender implementation needed
// by these tests; it does not exercise wire signing.
type fakeInboxSender struct{}

func (f *fakeInboxSender) Send(_ context.Context, _ outbounddelivery.Delivery) error { return nil }