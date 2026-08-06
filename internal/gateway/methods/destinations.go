package methods

import (
	"context"
	"sort"

	"github.com/nextlevelbuilder/goclaw/internal/gateway"
	"github.com/nextlevelbuilder/goclaw/internal/outbounddelivery"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

// DestinationsMethods handles outbound.destinations.list. It surfaces the
// outbound destinations registered at the composition root so the UI can
// offer them as cron-job delivery channels. A destination whose Sender is
// nil is reported with configured=false so operators see that the adapter
// is registered but not wired up (typically missing env vars).
type DestinationsMethods struct {
	destinations outbounddelivery.DestinationSet
}

func NewDestinationsMethods(d outbounddelivery.DestinationSet) *DestinationsMethods {
	return &DestinationsMethods{destinations: d}
}

func (m *DestinationsMethods) Register(router *gateway.MethodRouter) {
	router.Register(protocol.MethodOutboundDestinationsList, m.handleList)
}

func (m *DestinationsMethods) handleList(_ context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	names := m.destinations.Names()
	sort.Strings(names)

	out := make([]map[string]any, 0, len(names))
	for _, name := range names {
		out = append(out, map[string]any{
			"name":       name,
			"configured": m.destinations.Configured(name),
		})
	}

	client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{
		"destinations": out,
	}))
}