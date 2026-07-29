package outbounddelivery

import (
	"context"
	"errors"
	"testing"
)

type fakeSender struct {
	got Delivery
}

func (f *fakeSender) Send(_ context.Context, d Delivery) error {
	f.got = d
	return nil
}

func TestNewDestinationSet_Empty(t *testing.T) {
	s := NewDestinationSet()
	if s.Has("x") {
		t.Fatal("empty set must not contain entries")
	}
	if _, ok := s.Get("x"); ok {
		t.Fatal("Get on empty set must return false")
	}
	if names := s.Names(); len(names) != 0 {
		t.Fatalf("Names() = %v, want empty", names)
	}
}

func TestNewDestinationSet_NilReceiverSafe(t *testing.T) {
	var s DestinationSet
	if s.Has("x") {
		t.Fatal("nil set must not panic and must report no entries")
	}
	if _, ok := s.Get("x"); ok {
		t.Fatal("Get on nil set must return false")
	}
	if names := s.Names(); len(names) != 0 {
		t.Fatalf("Names() on nil set = %v, want empty", names)
	}
}

func TestNewDestinationSet_SkipsInvalidEntries(t *testing.T) {
	s := NewDestinationSet(
		Destination{Name: "", Sender: &fakeSender{}},
	)
	if s.Has("") {
		t.Fatal("empty Name must be skipped")
	}
	if names := s.Names(); len(names) != 0 {
		t.Fatalf("Names() = %v, want empty after invalid entries", names)
	}
}

func TestNewDestinationSet_KeepsNilSenderAsRegisteredButNotConfigured(t *testing.T) {
	s := NewDestinationSet(Destination{Name: "gemster_inbox", Sender: nil})
	d, ok := s.Get("gemster_inbox")
	if !ok {
		t.Fatal("a nil-Sender destination must remain registered")
	}
	if d.Sender != nil {
		t.Fatalf("Sender = %v, want nil to preserve configured-but-unconfigured state", d.Sender)
	}
}

func TestDestinationSet_GetReturnsConfiguredEntry(t *testing.T) {
	sender := &fakeSender{}
	s := NewDestinationSet(Destination{Name: "gemster_inbox", Sender: sender})
	d, ok := s.Get("gemster_inbox")
	if !ok {
		t.Fatal("Get must find registered destination")
	}
	if d.Name != "gemster_inbox" || d.Sender != sender {
		t.Fatalf("destination = %+v", d)
	}
}

func TestDestinationSet_HasAndNames(t *testing.T) {
	a := &fakeSender{}
	b := &fakeSender{}
	s := NewDestinationSet(
		Destination{Name: "a", Sender: a},
		Destination{Name: "b", Sender: b},
	)
	if !s.Has("a") || !s.Has("b") {
		t.Fatal("Has must find both registered destinations")
	}
	if s.Has("c") {
		t.Fatal("Has must not find missing destination")
	}
	got := s.Names()
	if len(got) != 2 {
		t.Fatalf("Names() = %v, want 2 entries", got)
	}
}

func TestRecipientResolverFunc_Adapter(t *testing.T) {
	wantErr := errors.New("rejected")
	fn := RecipientResolverFunc(func(userID, _ string) (string, error) {
		if userID == "" {
			return "", wantErr
		}
		return userID, nil
	})
	if _, err := fn.ResolveRecipient("", "direct"); !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	got, err := fn.ResolveRecipient("user@example.com", "direct")
	if err != nil || got != "user@example.com" {
		t.Fatalf("got = %q, err = %v", got, err)
	}
}