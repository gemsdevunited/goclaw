package outbounddelivery

// RecipientResolver is implemented by destinations that require
// adapter-specific validation or transformation of a producer-supplied
// user ID into a destination-shaped recipient address (e.g. an email).
// Implementations MUST be deterministic and side-effect free.
type RecipientResolver interface {
	ResolveRecipient(userID, peerKind string) (string, error)
}

// Destination is a single named outbound delivery route. Producers pick a
// destination by Name (for example via job.DeliverChannel); the Sender
// performs the actual delivery, and an optional Resolver validates the
// recipient before persistence.
type Destination struct {
	Name     string
	Sender   Sender
	Resolver RecipientResolver // optional; nil means accept any recipient.
}

// DestinationSet is a tiny name-keyed lookup over a fixed set of
// destinations built explicitly at boot. It is not a registry framework:
// no discovery, no hot-reload, no background workers. A nil DestinationSet
// is a valid empty set, so callers wired without destinations keep the
// same nil-handling they had before this abstraction existed.
type DestinationSet struct {
	byName map[string]Destination
}

// NewDestinationSet builds a set from an explicit list of destinations.
// Entries with an empty Name are skipped. A nil Sender is preserved: a
// destination whose Sender is nil is "registered but not configured",
// and callers receive the configured name while their dispatch path
// reports "<name> delivery is not configured".
func NewDestinationSet(dests ...Destination) DestinationSet {
	m := make(map[string]Destination, len(dests))
	for _, d := range dests {
		if d.Name == "" {
			continue
		}
		m[d.Name] = d
	}
	return DestinationSet{byName: m}
}

// Get returns the destination with the given name and whether it was found.
func (s DestinationSet) Get(name string) (Destination, bool) {
	if s.byName == nil {
		return Destination{}, false
	}
	d, ok := s.byName[name]
	return d, ok
}

// Has reports whether the set contains a destination with the given name.
func (s DestinationSet) Has(name string) bool {
	if s.byName == nil {
		return false
	}
	_, ok := s.byName[name]
	return ok
}

// Names returns the registered destination names in unspecified order.
func (s DestinationSet) Names() []string {
	if s.byName == nil {
		return nil
	}
	out := make([]string, 0, len(s.byName))
	for n := range s.byName {
		out = append(out, n)
	}
	return out
}

// RecipientResolverFunc adapts a plain function to the RecipientResolver
// interface so adapters can supply a resolver without declaring a type.
type RecipientResolverFunc func(userID, peerKind string) (string, error)

// ResolveRecipient implements RecipientResolver.
func (f RecipientResolverFunc) ResolveRecipient(userID, peerKind string) (string, error) {
	return f(userID, peerKind)
}