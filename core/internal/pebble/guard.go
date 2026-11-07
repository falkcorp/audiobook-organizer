// file: core/internal/pebble/guard.go
// version: 1.0.0
// guid: 04dfddc3-a208-481d-a068-256c6170f596
// last-edited: 2026-11-07

package pebble

// Guard is the one store-guard type of 05-storage §5S.3.7 ("One
// implementation"), instantiated twice: the per-owner level vector (compare
// MinReader) and, for books, the legacy projection (compare LegacyExact).
// It decides from a stamp read out of the store and its sidecar; it writes
// nothing.
type Guard struct {
	// StampSource and SidecarSource name the two inputs in a refusal:
	// "stamp"/"sidecar" for the level vector, "legacy-stamp" for the
	// projection.
	StampSource, SidecarSource string
	// Compare reports whether a store value is too new for what the build
	// supports.
	Compare func(store, supported int) bool
}

// MinReader refuses a store whose level is above the build's supported
// level: levels are minimum reader capabilities.
func MinReader(store, supported int) bool { return store > supported }

// LegacyExact refuses a legacy projection stamp above the value this build
// would write: the projection never decides anything else for a core build
// (§5S.3.7 "Legacy projection").
func LegacyExact(store, wouldWrite int) bool { return store > wouldWrite }

// Decision is one owner's guard outcome.
type Decision struct {
	// Store is the value the too-new check used: max(stamp, sidecar), so a
	// sidecar ahead of its stamp (a crash inside a level step) refuses too.
	Store int
	// Source names where Store came from.
	Source string
	// TooNew is the refusal.
	TooNew bool
	// Disagree reports a stamp and a sidecar that differ, which the boot log
	// prints.
	Disagree bool
}

// Decide applies the guard to one owner's stamp and sidecar values; an
// absent input counts as 0 (absent stamp = level 0).
func (g Guard) Decide(stamp int, stampOK bool, sidecar int, sidecarOK bool, supported int) Decision {
	d := Decision{Store: stamp, Source: g.StampSource}
	if !stampOK {
		d.Store = 0
	}
	if sidecarOK && sidecar > d.Store {
		d.Store, d.Source = sidecar, g.SidecarSource
	}
	d.Disagree = stampOK && sidecarOK && stamp != sidecar
	d.TooNew = g.Compare(d.Store, supported)
	return d
}
