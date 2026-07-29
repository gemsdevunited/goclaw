package outbounddelivery

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

type recordingSender struct {
	delivery Delivery
}

func (s *recordingSender) Send(_ context.Context, delivery Delivery) error {
	s.delivery = delivery
	return nil
}

func TestSenderAcceptsNeutralDelivery(t *testing.T) {
	delivery := Delivery{
		ID:         "delivery-1",
		TenantID:   uuid.New(),
		Recipient:  "opaque-recipient",
		SourceKind: SourceCron,
		SourceID:   "execution-1",
		Title:      "Report",
		Body:       "Report body",
		CreatedAt:  time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC),
	}
	sender := &recordingSender{}

	if err := sender.Send(context.Background(), delivery); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if sender.delivery != delivery {
		t.Fatalf("delivery = %#v, want %#v", sender.delivery, delivery)
	}
}
