// file: internal/serviceregistry/registry.go
// version: 1.2.0
// last-edited: 2026-11-08

package serviceregistry

// ServiceDef describes one service in a container's factory list. A package
// exports its defs as ServiceDefs() and the composition root passes the whole
// list to NewContainerFrom.
type ServiceDef struct {
	// Name is the registry key. Must be unique. Convention: lowercase,
	// dot-separated for grouping (e.g. "dedup", "metafetch", "itunes.batcher").
	Name string

	// Needs lists names of OTHER services this service's Build func will
	// Get[T]. The container enforces that Build can only Get services listed
	// here. Needs is the single source of truth for the build-time dep graph.
	Needs []string

	// Groups lists zero or more named groups this service belongs to. It is
	// documentation only: no container reads it since R1-34 removed
	// group-based inclusion, and the list the composition root passes decides
	// what is built. It goes with the defs themselves (06 §6.11.1 step 4).
	//
	// Conventions used today:
	//   "core"      — always-on infrastructure (W1 leaf + W2 cross-wired)
	//   "ai"        — embedding/AI cluster (each Build is config-gated)
	//   "activity"  — activity log + sidecar store (DatabasePath-gated)
	//   "plugins"   — UOS plugin registrations (dedup/acoustid/deluge/...)
	//   "scheduler" — opregistry, updatescheduler, batchpoller, watchers
	//
	// A service can belong to multiple groups. Audit a group with
	//   grep -rn 'Groups.*"core"' internal/ --include="*.go"
	Groups []string

	// Build constructs the service instance. May call Get[T](c, name) for
	// any name in Needs.
	Build func(c *Container) (any, error)
}
