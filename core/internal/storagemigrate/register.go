// file: core/internal/storagemigrate/register.go
// version: 1.0.0
// guid: 93cd8036-cbc4-4094-9d3f-c84bc051d065
// last-edited: 2026-11-10

package storagemigrate

import (
	"bytes"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
	"github.com/falkcorp/mediacore/storage/keys"
)

// migrationName is the <name> of an ID "<owner>.<name>": the task-kind
// segment grammar, so a module's kind "<module>.datamigrate.<name>" would
// always parse (§5S.3.6).
func migrationName() *regexp.Regexp { return regexp.MustCompile(`^[a-z][a-z0-9-]*$`) }

// register is runner rule 1 for one declaration: every problem it has, or
// the migration with its derived source handle and ranges.
func register(owners map[storage.Owner]storage.OwnerSpec, m datamigrate.DataMigration) (*migration, []error) {
	var probs []error
	bad := func(format string, args ...any) {
		probs = append(probs, fmt.Errorf("%w: %s: %s", datamigrate.ErrInvalidMigration, m.ID, fmt.Sprintf(format, args...)))
	}
	name, ok := strings.CutPrefix(m.ID, string(m.Owner)+".")
	if m.Owner == "" || !ok || !migrationName().MatchString(name) {
		bad("ID is not %q followed by a name of [a-z][a-z0-9-]*", string(m.Owner)+".")
	}
	os, registered := owners[m.Owner]
	if !registered {
		bad("owner %q is not a storage owner of this build", m.Owner)
	}
	switch m.Mode {
	case datamigrate.BootBlocking:
	case datamigrate.Online: // refused: a deferred-build member (SIMP5-7)
		probs = append(probs, fmt.Errorf("%w: %s: %w", datamigrate.ErrInvalidMigration, m.ID, datamigrate.ErrOnlineDeferred)) // the refusal names SIMP5-7
	case datamigrate.ModeInvalid:
		bad("Mode is zero (declare BootBlocking)")
	default:
		bad("unknown Mode %d", m.Mode)
	}
	if err := m.Parallelism.Validate(); err != nil {
		probs = append(probs, fmt.Errorf("%w: %s: Parallelism %s: %w", datamigrate.ErrInvalidMigration, m.ID, m.Parallelism, err))
	}
	switch m.Compat {
	case storage.Additive, storage.Breaking:
	case storage.CompatInvalid:
		bad("Compat is zero (declare Additive or Breaking)")
	default:
		bad("unknown Compat %d", m.Compat)
	}
	switch {
	case m.RaisesLevel < 0:
		bad("RaisesLevel %d is negative", m.RaisesLevel)
	case m.Compat == storage.Breaking && !m.Cutover && m.RaisesLevel == 0:
		bad("a Breaking migration raises its owner's level (RaisesLevel > 0, §5S.3.7)")
	case registered && m.RaisesLevel > os.Spec.Level:
		bad("RaisesLevel %d is above the level %d the build declares for %s, so the guard would refuse the build's own stamp", m.RaisesLevel, os.Spec.Level, m.Owner)
	}
	if m.Barrier < 0 {
		bad("Barrier %d is negative", m.Barrier)
	}
	if m.Step == nil {
		bad("no Step")
	}
	if len(m.Writes) > 0 {
		probs = append(probs, fmt.Errorf("%w: %s: Writes is refused: it is deferred with the Online mode (SIMP5-7)", datamigrate.ErrInvalidMigration, m.ID))
	}
	if m.Cutover {
		if m.Compat != storage.Breaking {
			bad("a Cutover step is Breaking (§5S.3.13)")
		}
	} else {
		for _, f := range []struct {
			name string
			set  bool
		}{
			{"Sensitive", len(m.Sensitive) > 0}, {"Residual", len(m.Residual) > 0}, {"Preflight", m.Preflight != nil},
			{"FixBlocker", m.FixBlocker != nil}, {"DeleteBlocker", m.DeleteBlocker != nil},
		} {
			if f.set {
				bad("%s is for Cutover steps only", f.name)
			}
		}
	}
	if !registered {
		return nil, probs
	}
	mg := &migration{m: m}
	probs = append(probs, mg.resolveSource(os)...)
	if len(probs) > 0 {
		return nil, probs
	}
	return mg, nil
}

// resolveSource checks Source and sets the source handle and the
// handle-relative ranges: every range lies in the owner's keyspace, or
// every range lies in one of the owner's legacy claims (one handle per
// migration, so Step's keys have one meaning). A Cutover step's ranges lie
// in ClaimRetired claims, except its Residual keys, each one exact-key range
// in a ClaimReadWrite claim (§5S.3.13).
func (mg *migration) resolveSource(os storage.OwnerSpec) []error {
	m := mg.m
	var probs []error
	bad := func(format string, args ...any) {
		probs = append(probs, fmt.Errorf("%w: %s: %s", datamigrate.ErrInvalidMigration, m.ID, fmt.Sprintf(format, args...)))
	}
	if len(m.Source) == 0 {
		bad("no Source")
		return probs
	}
	src := slices.Clone(m.Source)
	slices.SortFunc(src, func(a, b storage.Range) int { return bytes.Compare(a.Start, b.Start) })
	for i, r := range src {
		if r.End == nil || bytes.Compare(r.Start, r.End) >= 0 {
			bad("Source range [%q, %q) is empty or unbounded", r.Start, r.End)
			return probs
		}
		if i > 0 && bytes.Compare(src[i-1].End, r.Start) > 0 {
			bad("Source ranges [%q, %q) and [%q, %q) overlap", src[i-1].Start, src[i-1].End, r.Start, r.End)
			return probs
		}
	}
	prefix := keyspacePrefix(m.Owner)
	inKeyspace := os.Spec.Keyspace && prefix != nil
	for _, r := range src {
		inKeyspace = inKeyspace && within(r, prefix)
	}
	if inKeyspace {
		if m.Cutover {
			bad("a Cutover step's Source lies in retired legacy claims, not in the owner's keyspace (§5S.3.13)")
			return probs
		}
		mg.prefix = prefix
		end := keys.PrefixEnd(prefix)
		for _, r := range src {
			rel := storage.Range{Start: r.Start[len(prefix):]}
			if !bytes.Equal(r.End, end) {
				rel.End = r.End[len(prefix):]
			}
			mg.source = append(mg.source, rel)
		}
		return probs
	}
	residual := map[string]bool{}
	for _, k := range m.Residual {
		residual[string(k)] = true
	}
	covered := map[string]bool{}
	for _, r := range src {
		c, ok := governing(os.Spec.Claims, r)
		switch {
		case !ok:
			bad("Source range [%q, %q) lies neither in %s's keyspace nor inside one of its claims (or crosses a claim boundary or a carve-out)", r.Start, r.End, m.Owner)
		case !m.Cutover:
		case c.Mode == storage.ClaimRetired:
		case c.Mode == storage.ClaimReadWrite && exactKey(r) && residual[string(r.Start)]:
			covered[string(r.Start)] = true
		default:
			bad("Source range [%q, %q) is in a %s claim: a Cutover step's Source is retired claims only, plus its Residual keys", r.Start, r.End, c.Mode)
		}
	}
	for _, k := range m.Residual {
		if !covered[string(k)] {
			bad("Residual key %q is not an exact-key Source range in a read-write claim", k)
		}
	}
	if len(probs) == 0 {
		mg.legacy = true
		mg.source = src
	}
	return probs
}

// keyspacePrefix is owner's keyspace prefix, "m/<id>/" or "c/<sub>/" (as
// storage derives it); nil for an owner storage would refuse.
func keyspacePrefix(o storage.Owner) []byte {
	if sub, ok := strings.CutPrefix(string(o), "core/"); ok {
		if keys.ValidName(sub) != nil {
			return nil
		}
		return []byte("c/" + sub + "/")
	}
	if o == "" || strings.Contains(string(o), "/") {
		return nil
	}
	return []byte("m/" + string(o) + "/")
}

// within reports whether r lies in the keys that start with prefix.
func within(r storage.Range, prefix []byte) bool {
	end := keys.PrefixEnd(prefix)
	return bytes.HasPrefix(r.Start, prefix) && (bytes.HasPrefix(r.End, prefix) || bytes.Equal(r.End, end))
}

// claimSpan is c's absolute span [start, end).
func claimSpan(c storage.Claim) storage.Range {
	if c.Key != "" {
		return storage.Range{Start: []byte(c.Key), End: append([]byte(c.Key), 0)}
	}
	return storage.Range{Start: []byte(c.Prefix), End: keys.PrefixEnd([]byte(c.Prefix))}
}

// contains reports whether outer holds every key of r.
func contains(outer, r storage.Range) bool {
	return bytes.Compare(outer.Start, r.Start) <= 0 && bytes.Compare(r.End, outer.End) <= 0
}

// overlaps reports whether a and b share a key.
func overlaps(a, b storage.Range) bool {
	return bytes.Compare(a.Start, b.End) < 0 && bytes.Compare(b.Start, a.End) < 0
}

// exactKey reports whether r holds exactly one key.
func exactKey(r storage.Range) bool {
	return len(r.End) == len(r.Start)+1 && r.End[len(r.End)-1] == 0 && bytes.HasPrefix(r.End, r.Start)
}

// governing is the claim every key of r resolves to (storage's longest
// match): the deepest of the owner's claims that holds all of r, when no
// deeper claim of the owner cuts into r and r meets none of its carve-outs
// (another owner's keys).
func governing(claims []storage.Claim, r storage.Range) (storage.Claim, bool) {
	best, depth := storage.Claim{}, -1
	for _, c := range claims {
		if d := claimDepth(c); contains(claimSpan(c), r) && d > depth {
			best, depth = c, d
		}
	}
	if depth < 0 {
		return storage.Claim{}, false
	}
	for _, c := range claims {
		if claimDepth(c) > depth && overlaps(claimSpan(c), r) {
			return storage.Claim{}, false
		}
	}
	for _, e := range best.Except {
		span := storage.Range{Start: []byte(e), End: keys.PrefixEnd([]byte(e))}
		if overlaps(span, r) {
			return storage.Claim{}, false
		}
	}
	return best, true
}

// claimDepth orders nested claims as storage's longest match does: an exact
// key is deeper than every prefix it lies under.
func claimDepth(c storage.Claim) int {
	if c.Key != "" {
		return len(c.Key) + 1
	}
	return len(c.Prefix)
}

// joinProblems is registration's one error: every problem, each matchable
// with errors.Is.
func joinProblems(probs []error) error {
	return &RegistrationError{Problems: probs}
}

// RegistrationError lists every problem registration found. errors.Is
// matches datamigrate.ErrInvalidMigration, and tasks.ErrNoParallelism or
// datamigrate.ErrOnlineDeferred when a problem is that refusal. Name coined by
// F2-06.
type RegistrationError struct{ Problems []error }

func (e *RegistrationError) Error() string {
	parts := make([]string, len(e.Problems))
	for i, p := range e.Problems {
		parts[i] = p.Error()
	}
	return fmt.Sprintf("storagemigrate: %d problem(s): %s", len(parts), strings.Join(parts, "; "))
}

// Unwrap yields every problem.
func (e *RegistrationError) Unwrap() []error { return slices.Clone(e.Problems) }
