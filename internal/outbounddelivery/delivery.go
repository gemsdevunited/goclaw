package outbounddelivery

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// SourceCron identifies a delivery produced by a cron execution.
const SourceCron = "CRON"

// Delivery is the neutral payload passed from a producer to a delivery adapter.
// Recipient is intentionally opaque; each adapter defines its address semantics.
type Delivery struct {
	ID         string
	TenantID   uuid.UUID
	Recipient  string
	SourceKind string
	SourceID   string
	Title      string
	Body       string
	CreatedAt  time.Time
}

// Sender delivers a producer payload through an external destination.
type Sender interface {
	Send(ctx context.Context, delivery Delivery) error
}
