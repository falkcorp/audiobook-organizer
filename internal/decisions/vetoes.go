// file: internal/decisions/vetoes.go
// version: 1.1.0
// guid: 6a2e90c7-35d1-4b48-9f6e-d17b83a40c52
// last-edited: 2026-10-21

package decisions

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

var dlog = logger.New("decisions")

// IDResolver maps an entity id to the id that survives merges: an author id
// that was merged away resolves to its canonical author. It returns id
// unchanged when there is no redirect.
type IDResolver func(entityType, id string) (string, error)

// maxTombstoneHops bounds a redirect chain; ResolveTombstoneChains keeps them
// at one hop, so more than this means a cycle.
const maxTombstoneHops = 16

// AuthorTombstoneResolver resolves "author" ids through author_tombstone:
// redirects. Other entity types and non-numeric ids resolve to themselves.
//
// BOOK MERGE REDIRECTS ARE NOT RESOLVED HERE YET, and a snapshot built without
// one fails OPEN for a merged-away book id: a pair rejected as (A, B) is not
// seen as vetoed once A has been merged into C. A resolver for "book" has to
// follow the merge survivor (merge.ResolveMergeSurvivor, which weighs the sync
// redirect against MergedIntoBookID) and that package sits at the same layer
// as this one, so the caller composes it into an IDResolver; it is required
// before the first producer that proposes book pairs (PR3).
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
		return "", fmt.Errorf("decisions: author %s: tombstone chain longer than %d hops", id, maxTombstoneHops)
	}
}

type pairID struct{ entityType, lo, hi string }
type entityID struct{ entityType, id string }
type famRec struct{ family, recordID string }

// rejected is what a live reject leaves on a subject: the hashes it blocks
// (none: sticky, blocks whatever the data).
type rejected struct{ hashes []string }

// Vetoes is an immutable snapshot of every live veto, loaded once per plan
// run so lookups cost a map read instead of a scan. It answers
// SubjectRejected and PairVetoed.
//
// FAIL CLOSED: Load returns an error when the store cannot be read, and
// PairVetoed returns one when an id cannot be resolved or a family is unknown.
// A caller must treat either as "skip the row" (the plan's
// decision_check_failed), never as "no veto".
type Vetoes struct {
	resolve  IDResolver
	subjects map[string]rejected            // opKind\x00subject -> live reject
	edges    map[pairID]map[string]struct{} // families
	members  map[entityID]map[famRec]struct{}
}

// Load reads every live reject (the decision:va: rows), pair edge and set
// membership with paged scans (never the unbounded ScanPrefix). resolve may be
// nil.
//
// POINT IN TIME. The three scans read ONE snapshot of the store, so a record
// that commits while Load runs (a re-reject that moves a pair from one family
// to another deletes one edge and writes another in one batch) is seen whole
// or not at all, never half: without that, Load could read the subject as
// rejected but miss both of its edges.
//
// Stored ids are resolved too: an edge recorded as (7, 9) still vetoes the
// pair after 9 is merged into 4, because the snapshot holds (4, 7). An edge
// whose two ends resolve to the same entity is dropped: they were merged
// anyway and there is no pair left to veto.
func (s *Store) Load(ctx context.Context, resolve IDResolver) (*Vetoes, error) {
	if resolve == nil {
		resolve = func(_, id string) (string, error) { return id, nil }
	}
	snap, err := s.kv.NewRawSnapshot()
	if err != nil {
		return nil, fmt.Errorf("decisions: open snapshot: %w", err)
	}
	defer func() { _ = snap.Close() }()

	v := &Vetoes{
		resolve:  resolve,
		subjects: map[string]rejected{},
		edges:    map[pairID]map[string]struct{}{},
		members:  map[entityID]map[famRec]struct{}{},
	}
	err = scanAll(ctx, snap, prefixActive, func(p database.KVPair) error {
		opKind, subject, ok := parseActiveKey(p.Key)
		if !ok {
			return fmt.Errorf("%w: unparseable active key %q", ErrCorrupt, p.Key)
		}
		var row ActiveRow
		if err := json.Unmarshal(p.Value, &row); err != nil {
			return fmt.Errorf("%w: decode active row %s: %w", ErrCorrupt, p.Key, err)
		}
		v.subjects[subjectLockKey(opKind, subject)] = rejected{hashes: row.RejectedHashes}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := scanAll(ctx, snap, prefixEdge, func(p database.KVPair) error {
		typ, lo, hi, fam, ok := parseEdgeKey(p.Key)
		if !ok {
			return fmt.Errorf("%w: unparseable edge key %q", ErrCorrupt, p.Key)
		}
		return v.addEdge(typ, lo, hi, fam)
	}); err != nil {
		return nil, err
	}
	if err := scanAll(ctx, snap, prefixMember, func(p database.KVPair) error {
		typ, id, fam, rid, ok := parseMemberKey(p.Key)
		if !ok {
			return fmt.Errorf("%w: unparseable membership key %q", ErrCorrupt, p.Key)
		}
		rid2, err := resolve(typ, id)
		if err != nil {
			return err
		}
		k := entityID{typ, rid2}
		if v.members[k] == nil {
			v.members[k] = map[famRec]struct{}{}
		}
		v.members[k][famRec{fam, rid}] = struct{}{}
		return nil
	}); err != nil {
		return nil, err
	}
	return v, nil
}

func (v *Vetoes) addEdge(typ, a, b, family string) error {
	ra, err := v.resolve(typ, a)
	if err != nil {
		return err
	}
	rb, err := v.resolve(typ, b)
	if err != nil {
		return err
	}
	if ra == rb {
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

// SubjectRejected reports whether the proposal (opKind, subjectKey) must be
// skipped. It applies the same rule as Store.Lookup: a live reject blocks the
// proposal whenever its material is a rejected one, or the reject is sticky,
// or materialHash is "" (a sticky op, whose data cannot be told apart). Only a
// proposal whose hash was never rejected is a new suggestion (reopened: never
// blocked here, never auto-applied by the caller). subjectKey is
// canonicalised; a malformed one is an error, never "not rejected".
func (v *Vetoes) SubjectRejected(opKind, subjectKey, materialHash string) (bool, error) {
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
// error. A pair whose ends resolve to one entity is not a pair and is not
// vetoed.
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
	ra, err := v.resolve(entityType, a)
	if err != nil {
		return false, err
	}
	rb, err := v.resolve(entityType, b)
	if err != nil {
		return false, err
	}
	if ra == rb {
		return false, nil
	}
	lo, hi := orderPair(ra, rb)
	for f := range v.edges[pairID{entityType, lo, hi}] {
		if familyMatches(f, family) {
			return true, nil
		}
	}
	ma, mb := v.members[entityID{entityType, ra}], v.members[entityID{entityType, rb}]
	if len(mb) < len(ma) {
		ma, mb = mb, ma
	}
	for fr := range ma {
		if _, ok := mb[fr]; ok && familyMatches(fr.family, family) {
			return true, nil
		}
	}
	return false, nil
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
