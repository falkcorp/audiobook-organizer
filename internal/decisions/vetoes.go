// file: internal/decisions/vetoes.go
// version: 1.0.0
// guid: 6a2e90c7-35d1-4b48-9f6e-d17b83a40c52
// last-edited: 2026-10-21

package decisions

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// IDResolver maps an entity id to the id that survives merges: an author id
// that was merged away resolves to its canonical author. It returns id
// unchanged when there is no redirect.
type IDResolver func(entityType, id string) (string, error)

// maxTombstoneHops bounds a redirect chain; ResolveTombstoneChains keeps them
// at one hop, so more than this means a cycle.
const maxTombstoneHops = 16

// AuthorTombstoneResolver resolves "author" ids through author_tombstone:
// redirects. Other entity types and non-numeric ids resolve to themselves.
// (Book merge redirects have no store API yet; a later PR adds them here.)
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
			if next == 0 || next == n {
				return strconv.Itoa(n), nil
			}
			n = next
		}
		return "", fmt.Errorf("decisions: author %s: tombstone chain longer than %d hops", id, maxTombstoneHops)
	}
}

type pairID struct{ entityType, lo, hi string }
type entityID struct{ entityType, id string }
type famRec struct{ family, recordID string }

// Vetoes is an immutable snapshot of every active veto, loaded once per plan
// run so lookups cost a map read instead of a scan. It answers
// SubjectRejected and PairVetoed.
//
// FAIL CLOSED: Load returns an error when the store cannot be read, and
// PairVetoed returns one when an id cannot be resolved. A caller must treat
// either as "skip the row" (the plan's decision_check_failed), never as "no
// veto".
type Vetoes struct {
	resolve  IDResolver
	subjects map[string]string              // opKind\x00subject -> rejected MaterialHash
	edges    map[pairID]map[string]struct{} // families
	members  map[entityID]map[famRec]struct{}
}

// Load reads every active verdict, pair edge and set membership with paged
// scans (never the unbounded ScanPrefix). resolve may be nil.
//
// Stored ids are resolved too: an edge recorded as (7, 9) still vetoes the
// pair after 9 is merged into 4, because the snapshot holds (4, 7). An edge
// whose two ends resolve to the same entity is dropped: they were merged
// anyway and there is no pair left to veto.
func (s *Store) Load(ctx context.Context, resolve IDResolver) (*Vetoes, error) {
	if resolve == nil {
		resolve = func(_, id string) (string, error) { return id, nil }
	}
	v := &Vetoes{
		resolve:  resolve,
		subjects: map[string]string{},
		edges:    map[pairID]map[string]struct{}{},
		members:  map[entityID]map[famRec]struct{}{},
	}
	err := s.scanAll(ctx, prefixVerdict, func(p database.KVPair) error {
		var vd Verdict
		if err := unmarshalVerdict(p, &vd); err != nil {
			return err
		}
		if vd.Blocks() {
			v.subjects[subjectLockKey(vd.OpKind, vd.SubjectKey)] = vd.MaterialHash
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := s.scanAll(ctx, prefixEdge, func(p database.KVPair) error {
		typ, lo, hi, fam, ok := parseEdgeKey(p.Key)
		if !ok {
			return fmt.Errorf("%w: unparseable edge key %q", ErrCorrupt, p.Key)
		}
		return v.addEdge(typ, lo, hi, fam)
	}); err != nil {
		return nil, err
	}
	if err := s.scanAll(ctx, prefixMember, func(p database.KVPair) error {
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
// skipped. It applies the same rule as Store.Lookup: an active reject blocks,
// unless it recorded a material hash and the proposal as it would be made now
// (materialHash, "" for a sticky op) has a different one, in which case the
// data changed and the proposal is reopened (never blocked here, never
// auto-applied by the caller).
func (v *Vetoes) SubjectRejected(opKind, subjectKey, materialHash string) bool {
	rejected, ok := v.subjects[subjectLockKey(opKind, subjectKey)]
	if !ok {
		return false
	}
	return !(rejected != "" && materialHash != "" && rejected != materialHash)
}

// PairVetoed reports whether joining a and b in family is vetoed by an active
// reject (a reject stored with FamilyAny vetoes every family). A pair whose
// ends resolve to one entity is not a pair and is not vetoed.
func (v *Vetoes) PairVetoed(entityType, a, b, family string) (bool, error) {
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
	if fams := v.edges[pairID{entityType, lo, hi}]; fams != nil {
		if _, ok := fams[family]; ok {
			return true, nil
		}
		if _, ok := fams[FamilyAny]; ok {
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
