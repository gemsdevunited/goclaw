package gemsterinbox

import "github.com/nextlevelbuilder/goclaw/internal/outbounddelivery"

// NewDestination bundles the Gemster Inbox adapter's identifier, sender, and
// recipient resolver into a single outbounddelivery.Destination. The adapter
// owns its own destination name so cron core stays adapter-agnostic.
func NewDestination(sender outbounddelivery.Sender) outbounddelivery.Destination {
	return outbounddelivery.Destination{
		Name:     Destination,
		Sender:   sender,
		Resolver: outbounddelivery.RecipientResolverFunc(RecipientFor),
	}
}