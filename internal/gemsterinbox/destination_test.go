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

// fakeInboxSender is the minimum outbounddelivery.Sender implementation needed
// by these tests; it does not exercise wire signing.
type fakeInboxSender struct{}

func (f *fakeInboxSender) Send(_ context.Context, _ outbounddelivery.Delivery) error { return nil }