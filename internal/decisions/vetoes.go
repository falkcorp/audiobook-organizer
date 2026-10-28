// file: internal/decisions/vetoes.go
// version: 1.5.1
// guid: 6a2e90c7-35d1-4b48-9f6e-d17b83a40c52
// last-edited: 2026-10-28

package decisions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

var dlog = logger.New("decisions")

// IDResolver maps an entity id to the id that survives merges: an author id
// that was merged away resolves to its canonical author. It returns id
// unchanged when there is no redirect. What it returns is canonicalised
// (CanonicalID) before use; a result that is not a valid id is an
// UnresolvableIDError. An error that wraps ErrIDUnresolvable is the id's own
// data (see there); any other error is a failed read.
type IDResolver func(entityType, id string) (string, error)

// maxTombstoneHops bounds a redirect chain; ResolveTombstoneChains keeps them
// at one hop, so more than this means a cycle.
const maxTombstoneHops = 16

// AuthorTombstoneResolver resolves "author" ids through author_tombstone:
// redirects. Other entity types and non-numeric ids resolve to themselves.
//
// BOOK ids are not resolved here. A snapshot built without a book resolver
// fails OPEN for a merged-away or retired book id: a pair rejected as (A, B) is
// not seen as vetoed once A has been merged into C, or retired into C and
// purged. The book resolver is RetiredBookResolver (follows the retired-version
// index) chained with the merge survivor through ChainPathResolvers;
// internal/merge sits at the same layer as this package, so the caller that
// imports both composes them (maintenance.NewOwnerVetoReader does, for the
// retired-versions fixer). Any producer that proposes book pairs must load its
// snapshot with it (LoadPaths).
func AuthorTombstoneResolver(store interface {
	GetAuthorTombstone(oldID int) (int, error)
}) IDResolver {
	return func(entityType, id string) (string, error) {
		if entityType != "author" {
			return id, nil
		}
		n, err := strconv.Atoi(id)
		if err != nil {
			return id, nil
		}
		for hop := 0; hop < maxTombstoneHops; hop++ {
			next, err := store.GetAuthorTombstone(n)
			if err != nil {
				return "", fmt.Errorf("decisions: resolve author %d: %w", n, err)
			}
			if next == 0 {
				return strconv.Itoa(n), nil
			}
			if next == n {
				// A tombstone that redirects to itself is a one-author cycle:
				// treated as no redirect (the author is live), but never silent.
				dlog.Warn("author %s: tombstone chain starting here ends in a self-redirect at author %d",
					logger.SanitizeLogValue(id), n)
				return strconv.Itoa(n), nil
			}
			n = next
		}
		dlog.Error("author %s: tombstone chain longer than %d hops (cycle?); refusing to resolve it",
			logger.SanitizeLogValue(id), maxTombstoneHops)
		return "", &UnresolvableIDError{EntityType: "author", ID: id, Kind: ErrResolverCycle}
	}
}

type pairID struct{ entityType, lo, hi string }
type entityID struct{ entityType, id string }
type famRec struct{ family, recordID string }

// rejected is what a live reject leaves on a subject: the hashes it blocks
// (none: sticky, blocks whatever the data).
type rejected struct{ hashes []string }

// collapsedVeto is a stored pair veto whose two ends were merged into one book
// (they resolve to the same id). It is kept with the path each end took,
// because it still matters to a producer that proposes to JOIN books through
// one of the ids on those paths: see collapsedVeto.holds.
type collapsedVeto struct {
	family string
	px, py []string
}

// holds reports whether the veto stands against joining a and b, which resolve
// to the very id both ends of the veto resolve to. It does when one end's path
// passes through one of the two books (not through the other end's path too)
// and the other end's path reaches the other book: the join of a and b is then
// the join of the two vetoed books, or of books that stand for them.
//
// Both ends passing through the same book is not that: they were merged into
// it before the veto could matter. A veto whose ends merged into the id
// without touching a or b says nothing about them.
func (c collapsedVeto) holds(a, b string) bool {
	for _, q := range [2][2]string{{a, b}, {b, a}} {
		through, reached := q[0], q[1]
		for _, e := range [2][2][]string{{c.px, c.py}, {c.py, c.px}} {
			p, o := e[0], e[1]
			if slices.Contains(p, through) && !slices.Contains(o, through) && slices.Contains(o, reached) {
				return true
			}
		}
	}
	return false
}

// heldVeto is a stored veto that names an id that could not be resolved, so
// where it points is unknown. ok holds the footprints (every id on the path)
// of the ends that did resolve, bad those of the ends that did not (the id and
// whatever its failure involves: candidate holders, the ids of a cycle).
type heldVeto struct {
	entityType string
	family     string
	ok, bad    []map[string]struct{}
	ids        []UnresolvedID
}

// touches reports whether the veto could bear on a pair whose resolution
// paths cover reach. A veto can only join one of its OK ends' books to some
// book on the unresolved side, so anything that reaches an OK end is touched.
// With no OK end (every end unresolved) the best that is known is the
// unresolved ends' own footprints.
func (h *heldVeto) touches(reach map[string]struct{}) bool {
	fps := h.ok
	if len(fps) == 0 {
		fps = h.bad
	}
	for _, fp := range fps {
		for id := range reach {
			if _, ok := fp[id]; ok {
				return true
			}
		}
	}
	return false
}

// Vetoes is an immutable snapshot of every live veto, loaded once per plan
// run so lookups cost a map read instead of a scan. It answers
// SubjectRejected and PairVetoed.
//
// The rows are one point-in-time read of the decisions log. Ids are resolved
// at Load (the stored ids) and at each query (the ids asked about) through the
// resolver, which may read live state: a book merged between Load and a query
// is resolved as it is at the query. That is fail closed (a later resolution
// only joins more ids to a veto), but the snapshot is not one consistent
// instant for the resolver's reads.
//
// FAIL CLOSED: Load returns an error when the store cannot be read, and
// PairVetoed returns one when a family is unknown or a read fails. An id that
// cannot be resolved does not refuse the load: it is held per id (Unresolved
// lists them all), and PairVetoed returns an *UnresolvableError for a pair it
// touches, which a caller must treat as "hold the row", never as "no veto".
type Vetoes struct {
	resolve  PathResolver
	subjects map[string]rejected            // opKind\x00subject -> live reject
	edges    map[pairID]map[string]struct{} // families
	members  map[entityID]map[famRec]struct{}
	// collapsed keeps the vetoes whose ends resolved to one book, keyed by it.
	collapsed map[entityID][]collapsedVeto
	// held keeps the vetoes that name an unresolvable id.
	held       []heldVeto
	unresolved map[entityID]UnresolvedID
}

// Load reads every live reject (the decision:va: rows), pair edge and set
// membership with paged scans (never the unbounded ScanPrefix) and resolves
// their ids through resolve, which may be nil. See LoadPaths.
func (s *Store) Load(ctx context.Context, resolve IDResolver) (*Vetoes, error) {
	if resolve == nil {
		return s.LoadPaths(ctx, nil)
	}
	return s.LoadPaths(ctx, PathOf(resolve))
}

// LoadPaths is Load for a resolver that reports the path an id took. resolve
// may be nil.
//
// POINT IN TIME. The three scans read ONE snapshot of the store, so a record
// that commits while Load runs (a re-reject that moves a pair from one family
// to another deletes one edge and writes another in one batch) is seen whole
// or not at all, never half: without that, Load could read the subject as
// rejected but miss both of its edges. The raw rows are collected and the
// snapshot closed BEFORE any id is resolved, so tombstone reads (database
// reads of their own) never extend the snapshot's lease.
//
// Stored ids are resolved too: an edge recorded as (7, 9) still vetoes the
// pair after 9 is merged into 4, because the snapshot holds (4, 7).
//
// An edge whose two ends resolve to the same entity is not dropped: they were
// merged, so there is no pair of live books left to veto, but a producer
// proposing to join one of the ids on their paths to the other's is proposing
// the very join the owner rejected (retiring a merged-away shell onto its
// target is one). The edge is kept with both paths (collapsedVeto) and a query
// whose ends collapse to that book is checked against them.
//
// An id that cannot be resolved (ErrIDUnresolvable: an ambiguous shell, a
// cycle) does not fail the load. The veto that names it is held (heldVeto) and
// the id listed (Unresolved). Only a failed READ refuses the load.
func (s *Store) LoadPaths(ctx context.Context, resolve PathResolver) (*Vetoes, error) {
	if resolve == nil {
		resolve = func(_, id string) ([]string, error) { return []string{id}, nil }
	}
	rows, err := s.readRows(ctx)
	if err != nil {
		return nil, err
	}

	// The snapshot is closed by now. Resolving can read the database (author
	// tombstones), and those reads must not extend the snapshot's lease.
	v := &Vetoes{
		resolve:    resolve,
		subjects:   rows.subjects,
		edges:      map[pairID]map[string]struct{}{},
		members:    map[entityID]map[famRec]struct{}{},
		collapsed:  map[entityID][]collapsedVeto{},
		unresolved: map[entityID]UnresolvedID{},
	}
	for _, e := range rows.edges {
		if err := v.addEdge(e.typ, e.a, e.b, e.family); err != nil {
			return nil, err
		}
	}
	if err := v.addMembers(rows.members); err != nil {
		return nil, err
	}
	return v, nil
}

type rawEdge struct{ typ, a, b, family string }
type rawMember struct{ typ, id, family, recordID string }

// loadedRows is what one snapshot read collected, before any id is resolved.
type loadedRows struct {
	subjects map[string]rejected
	edges    []rawEdge
	members  []rawMember
}

// readRows returns the three veto families from ONE snapshot, closed before
// it returns. With a RowCache it is the cached read while no veto row has
// been written since; otherwise a fresh scan.
func (s *Store) readRows(ctx context.Context) (*loadedRows, error) {
	if s.cache == nil {
		return s.scanRows(ctx)
	}
	// The generation is read BEFORE the scan: a write that commits during the
	// scan bumps it afterwards, so rows scanned across a commit are cached
	// under a generation that is already stale.
	gen := logGeneration(s.lockScope)
	if rows := s.cache.get(s.lockScope, gen); rows != nil {
		return rows, nil
	}
	rows, err := s.scanRows(ctx)
	if err != nil {
		return nil, err
	}
	s.cache.put(s.lockScope, gen, rows)
	return rows, nil
}

// scanRows reads the three veto families from ONE snapshot and closes it
// before returning.
func (s *Store) scanRows(ctx context.Context) (*loadedRows, error) {
	snap, err := s.kv.NewRawSnapshot()
	if err != nil {
		return nil, fmt.Errorf("decisions: open snapshot: %w", err)
	}
	defer func() { _ = snap.Close() }()

	rows := &loadedRows{subjects: map[string]rejected{}}
	if err := scanAll(ctx, snap, prefixActive, func(p database.KVPair) error {
		opKind, subject, ok := parseActiveKey(p.Key)
		if !ok {
			return fmt.Errorf("%w: unparseable active key %q", ErrCorrupt, p.Key)
		}
		var row ActiveRow
		if err := json.Unmarshal(p.Value, &row); err != nil {
			return fmt.Errorf("%w: decode active row %s: %w", ErrCorrupt, p.Key, err)
		}
		rows.subjects[subjectLockKey(opKind, subject)] = rejected{hashes: row.RejectedHashes}
		return nil
	}); err != nil {
		return nil, err
	}
	if err := scanAll(ctx, snap, prefixEdge, func(p database.KVPair) error {
		typ, lo, hi, fam, ok := parseEdgeKey(p.Key)
		if !ok {
			return fmt.Errorf("%w: unparseable edge key %q", ErrCorrupt, p.Key)
		}
		rows.edges = append(rows.edges, rawEdge{typ, lo, hi, fam})
		return nil
	}); err != nil {
		return nil, err
	}
	if err := scanAll(ctx, snap, prefixMember, func(p database.KVPair) error {
		typ, id, fam, rid, ok := parseMemberKey(p.Key)
		if !ok {
			return fmt.Errorf("%w: unparseable membership key %q", ErrCorrupt, p.Key)
		}
		rows.members = append(rows.members, rawMember{typ, id, fam, rid})
		return nil
	}); err != nil {
		return nil, err
	}
	return rows, nil
}

// resolvedEnd is one stored id after resolution: its path, or why it has none.
type resolvedEnd struct {
	id   string
	path []string
	err  error // non-nil: the id is unresolvable (ErrIDUnresolvable)
}

func (e resolvedEnd) end() string { return e.path[len(e.path)-1] }

// resolvePath resolves one id and canonicalises every id on the path: a
// resolver is caller code, and a ULID it hands back in lower case, or with
// stray whitespace, would otherwise key a different pair than the stored
// (canonical) ids do. An empty path or an id that is not valid is an
// UnresolvableIDError.
func (v *Vetoes) resolvePath(entityType, id string) ([]string, error) {
	p, err := v.resolve(entityType, id)
	if err != nil {
		return nil, err
	}
	if len(p) == 0 {
		return nil, &UnresolvableIDError{EntityType: entityType, ID: id, Kind: ErrResolvedIDInvalid}
	}
	out := make([]string, len(p))
	for i, e := range p {
		c, err := CanonicalID(e)
		if err != nil {
			return nil, &UnresolvableIDError{EntityType: entityType, ID: id,
				Kind: fmt.Errorf("%w for %q: %w", ErrResolvedIDInvalid, e, err), Related: p}
		}
		out[i] = c
	}
	return out, nil
}

// resolveEnd resolves one id. An unresolvable id is a result (resolvedEnd.err);
// any other error (a failed read) is returned as the second value.
func (v *Vetoes) resolveEnd(entityType, id string) (resolvedEnd, error) {
	p, err := v.resolvePath(entityType, id)
	switch {
	case err == nil:
		return resolvedEnd{id: id, path: p}, nil
	case errors.Is(err, ErrIDUnresolvable):
		return resolvedEnd{id: id, err: err}, nil
	default:
		return resolvedEnd{}, err
	}
}

func footprint(ids ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if c, err := CanonicalID(id); err == nil {
			id = c
		}
		m[id] = struct{}{}
	}
	return m
}

// hold records a stored veto whose ends include an unresolvable one.
func (v *Vetoes) hold(entityType, family string, ends []resolvedEnd) {
	h := heldVeto{entityType: entityType, family: family}
	for _, e := range ends {
		if e.err == nil {
			h.ok = append(h.ok, footprint(e.path...))
			continue
		}
		fp := footprint(e.id)
		var ue *UnresolvableIDError
		if errors.As(e.err, &ue) {
			for id := range footprint(append([]string{ue.ID}, ue.Related...)...) {
				fp[id] = struct{}{}
			}
		}
		h.bad = append(h.bad, fp)
		u := UnresolvedID{EntityType: entityType, ID: e.id, Err: e.err}
		h.ids = append(h.ids, u)
		v.unresolved[entityID{entityType, e.id}] = u
	}
	v.held = append(v.held, h)
}

func (v *Vetoes) addEdge(typ, a, b, family string) error {
	ea, err := v.resolveEnd(typ, a)
	if err != nil {
		return err
	}
	eb, err := v.resolveEnd(typ, b)
	if err != nil {
		return err
	}
	if ea.err != nil || eb.err != nil {
		v.hold(typ, family, []resolvedEnd{ea, eb})
		return nil
	}
	ra, rb := ea.end(), eb.end()
	if ra == rb {
		k := entityID{typ, ra}
		v.collapsed[k] = append(v.collapsed[k], collapsedVeto{family: family, px: ea.path, py: eb.path})
		return nil
	}
	lo, hi := orderPair(ra, rb)
	k := pairID{typ, lo, hi}
	if v.edges[k] == nil {
		v.edges[k] = map[string]struct{}{}
	}
	v.edges[k][family] = struct{}{}
	return nil
}

// addMembers resolves the members of every rejected set. The members of one
// record veto each other pair by pair, so a set's members that resolve to one
// book are collapsed vetoes, exactly as a collapsed edge is.
func (v *Vetoes) addMembers(raw []rawMember) error {
	type recKey struct {
		typ string
		fr  famRec
	}
	groups := map[recKey][]resolvedEnd{}
	var order []recKey
	for _, m := range raw {
		e, err := v.resolveEnd(m.typ, m.id)
		if err != nil {
			return err
		}
		k := recKey{m.typ, famRec{m.family, m.recordID}}
		if groups[k] == nil {
			order = append(order, k)
		}
		groups[k] = append(groups[k], e)
	}
	for _, k := range order {
		ends := groups[k]
		byEnd := map[string][]resolvedEnd{}
		anyBad := false
		for _, e := range ends {
			if e.err != nil {
				anyBad = true
				continue
			}
			r := e.end()
			ek := entityID{k.typ, r}
			if v.members[ek] == nil {
				v.members[ek] = map[famRec]struct{}{}
			}
			v.members[ek][k.fr] = struct{}{}
			byEnd[r] = append(byEnd[r], e)
		}
		for r, grp := range byEnd {
			ek := entityID{k.typ, r}
			for i := range grp {
				for j := i + 1; j < len(grp); j++ {
					v.collapsed[ek] = append(v.collapsed[ek], collapsedVeto{family: k.fr.family, px: grp[i].path, py: grp[j].path})
				}
			}
		}
		if anyBad {
			v.hold(k.typ, k.fr.family, ends)
		}
	}
	return nil
}

// Unresolved lists every stored id the snapshot could not resolve, each once,
// ordered by entity type and id. The veto that names one is held: a pair it
// could touch is answered with an *UnresolvableError.
func (v *Vetoes) Unresolved() []UnresolvedID {
	out := make([]UnresolvedID, 0, len(v.unresolved))
	for _, u := range v.unresolved {
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].EntityType != out[j].EntityType {
			return out[i].EntityType < out[j].EntityType
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// SubjectRejected reports whether the proposal (opKind, subjectKey) must be
// skipped. It applies the same rule as Store.Lookup: a live reject blocks the
// proposal whenever its material is a rejected one, or the reject is sticky,
// or materialHash is "" (a sticky op, whose data cannot be told apart). Only a
// proposal whose hash was never rejected is a new suggestion (reopened: never
// blocked here, never auto-applied by the caller). subjectKey is
// canonicalised; a malformed one is an error, never "not rejected".
func (v *Vetoes) SubjectRejected(opKind, subjectKey, materialHash string) (bool, error) {
	if err := checkComponent("op_kind", opKind); err != nil {
		return false, err
	}
	subjectKey, err := CanonicalSubjectKey(subjectKey)
	if err != nil {
		return false, err
	}
	r, ok := v.subjects[subjectLockKey(opKind, subjectKey)]
	if !ok {
		return false, nil
	}
	return blocksMaterial(r.hashes, materialHash), nil
}

// PairVetoed reports whether joining a and b in family is vetoed by a live
// reject (a reject stored with FamilyAny vetoes every family; a query for
// FamilyAny asks about every family). Ids are canonicalised (a ULID is
// upper-cased; surrounding whitespace is an error) and an unknown family is an
// error.
//
// When a and b resolve to different entities, a stored veto between the two
// (or a set that holds both) answers it. When they resolve to ONE entity (a
// shell merged into its target, say) it is not a pair of live books, but the
// join it names may still be one the owner rejected: a collapsed veto answers
// it when its ends reach a and b along their paths (collapsedVeto.holds).
//
// An id that cannot be resolved is not a "no": if a or b cannot be resolved, or
// a stored veto that names an unresolvable id could touch the pair, the error
// is an *UnresolvableError listing every such id. A definite veto is reported
// as one even when unresolvable ids are also in play.
func (v *Vetoes) PairVetoed(entityType, a, b, family string) (bool, error) {
	if err := checkComponent("entity_type", entityType); err != nil {
		return false, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if err := checkQueryFamily(family); err != nil {
		return false, err
	}
	a, err := CanonicalID(a)
	if err != nil {
		return false, err
	}
	b, err = CanonicalID(b)
	if err != nil {
		return false, err
	}
	ea, err := v.resolveEnd(entityType, a)
	if err != nil {
		return false, err
	}
	eb, err := v.resolveEnd(entityType, b)
	if err != nil {
		return false, err
	}
	if ea.err != nil || eb.err != nil {
		var ids []UnresolvedID
		for _, e := range []resolvedEnd{ea, eb} {
			if e.err != nil {
				ids = append(ids, UnresolvedID{EntityType: entityType, ID: e.id, Err: e.err})
			}
		}
		return false, &UnresolvableError{What: "the pair asked about", IDs: ids}
	}
	ra, rb := ea.end(), eb.end()
	if ra == rb {
		for _, c := range v.collapsed[entityID{entityType, ra}] {
			if familyMatches(c.family, family) && c.holds(a, b) {
				return true, nil
			}
		}
	} else if v.pairStored(entityType, ra, rb, family) {
		return true, nil
	}
	return false, v.heldFor(entityType, family, ea.path, eb.path)
}

// pairStored answers PairVetoed for two different resolved books.
func (v *Vetoes) pairStored(entityType, ra, rb, family string) bool {
	lo, hi := orderPair(ra, rb)
	for f := range v.edges[pairID{entityType, lo, hi}] {
		if familyMatches(f, family) {
			return true
		}
	}
	ma, mb := v.members[entityID{entityType, ra}], v.members[entityID{entityType, rb}]
	if len(mb) < len(ma) {
		ma, mb = mb, ma
	}
	for fr := range ma {
		if _, ok := mb[fr]; ok && familyMatches(fr.family, family) {
			return true
		}
	}
	return false
}

// heldFor returns an *UnresolvableError naming every unresolved stored id whose
// veto could bear on a pair whose ends took paths pa and pb, nil for none.
func (v *Vetoes) heldFor(entityType, family string, pa, pb []string) error {
	if len(v.held) == 0 {
		return nil
	}
	reach := footprint(append(append([]string(nil), pa...), pb...)...)
	seen := map[entityID]bool{}
	var ids []UnresolvedID
	for i := range v.held {
		h := &v.held[i]
		if h.entityType != entityType || !familyMatches(h.family, family) || !h.touches(reach) {
			continue
		}
		for _, u := range h.ids {
			if k := (entityID{u.EntityType, u.ID}); !seen[k] {
				seen[k] = true
				ids = append(ids, u)
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].ID < ids[j].ID })
	return &UnresolvableError{What: "stored rejects that name them and could touch the pair", IDs: ids}
}

// Counts reports the snapshot's size, for logs and the census.
func (v *Vetoes) Counts() (subjects, pairs, setMembers int) {
	return len(v.subjects), len(v.edges), len(v.members)
}

// parseActiveKey splits decision:va:<op_kind>:<subject_key>.
func parseActiveKey(key string) (opKind, subjectKey string, ok bool) {
	parts := strings.Split(strings.TrimPrefix(key, prefixActive), ":")
	if len(parts) != 2 {
		return "", "", false
	}
	return unescapeComponent(parts[0]), unescapeComponent(parts[1]), true
}

// parseEdgeKey splits decision:pe:<type>:<lo>:<hi>:<family>:<record_id>.
func parseEdgeKey(key string) (typ, lo, hi, family string, ok bool) {
	parts := strings.Split(strings.TrimPrefix(key, prefixEdge), ":")
	if len(parts) != 5 {
		return "", "", "", "", false
	}
	return unescapeComponent(parts[0]), unescapeComponent(parts[1]), unescapeComponent(parts[2]), unescapeComponent(parts[3]), true
}

// parseMemberKey splits decision:mb:<type>:<id>:<family>:<record_id>.
func parseMemberKey(key string) (typ, id, family, recordID string, ok bool) {
	parts := strings.Split(strings.TrimPrefix(key, prefixMember), ":")
	if len(parts) != 4 {
		return "", "", "", "", false
	}
	return unescapeComponent(parts[0]), unescapeComponent(parts[1]), unescapeComponent(parts[2]), parts[3], true
}
