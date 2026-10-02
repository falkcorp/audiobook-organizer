// file: internal/database/catalog_entry_store.go
// version: 1.3.0
// guid: 0b7c4e91-5d2a-4f38-9e61-3a8d2f7c5b14
// last-edited: 2026-10-01

package database

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/oklog/ulid/v2"
)

// Author catalog store (design: .claude/notes/catalog-wanted-requests-design-
// 2026-10-01.md, Draft 3 R5/R6/R7/R10/R11).
//
// The catalog is every title a provider lists for an author the owner has.
// It is NOT a book table: nothing here is ever read by the scanner, dedup,
// ABS, the search index or the library stats, and nothing here references a
// local book or author row (R5: provider identity only, resolved at read
// time, because local author ids are retired by merges).
//
// Reads are Pebble-only (R11). The catalog is deliberately kept out of memdb
// warmup: ~200k estimated entries would cost more than the books table.
// Secondary keys below are the index; there is no in-memory copy.
//
// Keyspace (all new prefixes; dropping them removes the feature):
//
//	cat:<id>                         entry JSON (no raw payload)
//	cat_id:<id>                      ""    (every entry, keys only: list/count without reading entry values)
//	cat_raw:<id>                     provider JSON as received (never decoded on a read path)
//	cat_pid:<provider>:<mkt>:<pid>   -> id  (idempotent upsert)
//	cat_author:<author key>:<id>     ""    (author identity -> entries)
//	cat_series:<series key>:<id>     ""    (series -> entries)
//	cat_eg:<group id>:<id>           ""    (edition group -> entries)
//	cat_egkey:<group key>            -> group id (stable assignment, R6)
//	cat_hv:<harvest key>:<id>        ""    (which harvested author returned it)
//	cat_stale:<id>                   ""    (stale_since set; cheap census count)
//	cat_author_state:<harvest key>   per-author harvest state JSON (R10)
//
// cat_id was added before any live harvest ran (the op is behind
// catalog.enabled, default off), so no store holds entries without it and
// there is no backfill.
//
// Prefix collisions: "cat:" is distinct from every "cat_" prefix because the
// fifth byte differs (':' vs '_').
const (
	catEntryPrefix       = "cat:"
	catIDPrefix          = "cat_id:"
	catRawPrefix         = "cat_raw:"
	catPIDPrefix         = "cat_pid:"
	catAuthorPrefix      = "cat_author:"
	catSeriesPrefix      = "cat_series:"
	catGroupPrefix       = "cat_eg:"
	catGroupKeyPrefix    = "cat_egkey:"
	catHarvestPrefix     = "cat_hv:"
	catStalePrefix       = "cat_stale:"
	catAuthorStatePrefix = "cat_author_state:"
)

// CatalogKeyPrefixes lists every key prefix the catalog writes. Tests use it to
// prove a dry run writes nothing; a rollback can drop exactly these.
func CatalogKeyPrefixes() []string {
	return []string{catEntryPrefix, catIDPrefix, catRawPrefix, catPIDPrefix, catAuthorPrefix, catSeriesPrefix,
		catGroupPrefix, catGroupKeyPrefix, catHarvestPrefix, catStalePrefix, catAuthorStatePrefix}
}

// Edition kinds (owner answer Q1, 2026-10-01). Only an unabridged entry can
// fill a series gap in later phases, so "unknown" is the safe default.
const (
	EditionUnabridged = "unabridged"
	EditionAbridged   = "abridged"
	EditionDramatized = "dramatized"
	EditionFullCast   = "full_cast"
	EditionUnknown    = "unknown"
)

// CatalogEntryAuthor is an author credit as the provider gives it.
// ProviderAuthorID is the Audible author ASIN, empty when the provider did
// not supply one.
type CatalogEntryAuthor struct {
	Name             string `json:"name"`
	ProviderAuthorID string `json:"provider_author_id,omitempty"`
}

// CatalogEntrySeries is one series membership. Sequence is the provider's
// string verbatim; SeqLo/SeqHi are its parsed numeric range (R7), both nil
// when the sequence does not parse. A single position has SeqLo == SeqHi.
type CatalogEntrySeries struct {
	Name             string   `json:"name"`
	ProviderSeriesID string   `json:"provider_series_id,omitempty"`
	Sequence         string   `json:"sequence,omitempty"`
	SeqLo            *float64 `json:"seq_lo,omitempty"`
	SeqHi            *float64 `json:"seq_hi,omitempty"`
}

// CatalogEntry is one provider product (one ASIN) for an owned author.
type CatalogEntry struct {
	ID          string `json:"id"`
	Provider    string `json:"provider"`
	ProviderID  string `json:"provider_id"`
	Marketplace string `json:"marketplace"`
	Language    string `json:"language,omitempty"`

	Title     string               `json:"title"`
	Subtitle  string               `json:"subtitle,omitempty"`
	Authors   []CatalogEntryAuthor `json:"authors,omitempty"`
	Narrators []string             `json:"narrators,omitempty"`
	Series    []CatalogEntrySeries `json:"series,omitempty"`
	Publisher string               `json:"publisher,omitempty"`

	RuntimeMin  int    `json:"runtime_min,omitempty"`
	ReleaseDate string `json:"release_date,omitempty"`
	// Format is the provider's content_delivery_type (SinglePartBook,
	// MultiPartBook, PodcastParent...). FormatType is its format_type
	// (unabridged, abridged, original_recording).
	Format      string `json:"format,omitempty"`
	FormatType  string `json:"format_type,omitempty"`
	EditionKind string `json:"edition_kind"`
	CoverURL    string `json:"cover_url,omitempty"`

	// EditionGroupKey is the grouping key the entry was assigned under (R6);
	// EditionGroupID is the stable id assigned on first sight and never
	// recomputed by a later upsert. An entry with no author identity or no
	// title has no key and a singleton group.
	EditionGroupKey string `json:"edition_group_key,omitempty"`
	EditionGroupID  string `json:"edition_group_id"`

	// AuthorKeys and SeriesKeys are the index keys this entry is filed under.
	// The caller computes them (internal/catalog owns normalization); the
	// store only indexes what it is given.
	AuthorKeys []string `json:"author_keys,omitempty"`
	SeriesKeys []string `json:"series_keys,omitempty"`

	// ManualOnly marks Doctor Who / Big Finish / Torchwood (R13).
	ManualOnly bool `json:"manual_only,omitempty"`
	// NameOnlyAuthor: kept because the author NAME matched, without an
	// author ASIN confirming identity (R10). Derived: true when no current
	// harvester confirmed the entry by ASIN (ConfirmedBy is empty).
	NameOnlyAuthor bool `json:"name_only_author,omitempty"`
	// AuthorConflict: the owned author resolved to more than one author ASIN
	// and this entry matched one of them. Not a pick; a census item.
	// Derived: true when any current harvester saw a conflict.
	AuthorConflict bool `json:"author_conflict,omitempty"`
	// ConfirmedBy and ConflictBy are the per-harvester verdicts behind the
	// two flags above, as harvest keys (a subset of HarvestedBy). A
	// co-authored product is judged by each of its authors' harvests, and
	// those verdicts differ (one author has an owned ASIN-tagged book, the
	// other does not); storing only the flag made it last-writer-wins, so
	// the entry flipped between confirmed and name_only depending on which
	// worker finished last.
	ConfirmedBy []string `json:"confirmed_by,omitempty"`
	ConflictBy  []string `json:"conflict_by,omitempty"`

	// HarvestedBy is the set of harvest keys whose listing returned this
	// entry on their most recent fetch. A complete fetch that no longer
	// returns it removes its key; stale_since is set only when the set
	// empties, so a co-authored product never goes stale because one of its
	// authors' listings stopped returning it.
	HarvestedBy []string   `json:"harvested_by,omitempty"`
	FirstSeenAt time.Time  `json:"first_seen_at"`
	HarvestedAt time.Time  `json:"harvested_at"`
	HarvestOpID string     `json:"harvest_op_id,omitempty"`
	StaleSince  *time.Time `json:"stale_since,omitempty"`
}

// CatalogUpsert is one entry plus its raw provider payload.
type CatalogUpsert struct {
	Entry CatalogEntry
	Raw   []byte
}

// Harvest states (R10).
const (
	CatalogHarvestComplete = "complete"
	CatalogHarvestPartial  = "partial"
	CatalogHarvestFailed   = "failed"
)

// CatalogAuthorState is the per-author harvest record (R10). Key is the
// harvest key (folded author name): it has to be computable BEFORE any
// request, because it decides whether to make one. The resolved author ASINs
// are stored inside it.
type CatalogAuthorState struct {
	Key          string   `json:"key"`
	Name         string   `json:"name"`
	AuthorASINs  []string `json:"author_asins,omitempty"`
	State        string   `json:"state"`
	PagesDone    int      `json:"pages_done"`
	TotalResults int      `json:"total_results"`
	// Fetched is raw products received (the cap counts these, not kept ones).
	Fetched  int  `json:"fetched"`
	Kept     int  `json:"kept"`
	Dropped  int  `json:"dropped"`
	NameOnly int  `json:"name_only"`
	Capped   bool `json:"capped,omitempty"`
	// Skipped: products the provider sent that could not be decoded. They
	// count toward Fetched; a run that skipped any marks nothing stale.
	Skipped int `json:"skipped,omitempty"`
	// Duplicates: products the listing repeated across pages. A walk with
	// any is complete but marks nothing stale (a repeat stands in for an
	// omitted product).
	Duplicates int `json:"duplicates,omitempty"`
	// ShortKind/ShortRuns/ShortDelivered count consecutive runs whose
	// listing came back short the same way (same kind, same delivered
	// count). The harvest accepts a short listing after a fixed number of
	// agreeing runs instead of re-walking it forever; zero means the last
	// run was not short.
	ShortKind      string `json:"short_kind,omitempty"`
	ShortRuns      int    `json:"short_runs,omitempty"`
	ShortDelivered int    `json:"short_delivered,omitempty"`
	// Conflict: owned books named more than one author ASIN for this name.
	Conflict       bool       `json:"conflict,omitempty"`
	MarkedStale    int        `json:"marked_stale,omitempty"`
	LastAttemptAt  time.Time  `json:"last_attempt_at"`
	LastCompleteAt *time.Time `json:"last_complete_at,omitempty"`
	LastError      string     `json:"last_error,omitempty"`
	OpID           string     `json:"op_id,omitempty"`
}

// CatalogStore is the Pebble-backed catalog. Safe for concurrent use.
type CatalogStore struct {
	db *pebble.DB
	// writeMu serializes every upsert and stale pass. It spans the pid
	// lookup, the group-key assignment and the batch commit: two harvest
	// workers handling co-authors can both see the same ASIN, and without
	// one lock across lookup+commit each would mint its own entry id (and
	// its own edition group id) for it. -race cannot see that; it is a
	// logical race, not a memory one.
	writeMu sync.Mutex
	now     func() time.Time
	// scanHook, when set (tests only), is told every prefix a list or count
	// scan walks, so a test can prove the entry values are never iterated.
	scanHook func(prefix string)
}

// NewCatalogStore builds the catalog store on a shared PebbleDB handle.
func NewCatalogStore(db *pebble.DB) *CatalogStore {
	return &CatalogStore{db: db, now: time.Now}
}

// NewCatalogStoreFromStore builds the catalog store from any store that
// resolves to a *PebbleStore through the decorator chain, or returns nil.
func NewCatalogStoreFromStore(store any) *CatalogStore {
	ps := AsPebbleStore(store)
	if ps == nil {
		return nil
	}
	return NewCatalogStore(ps.DB())
}

// ErrCatalogEntryNotFound is returned by GetEntry for an unknown id.
var ErrCatalogEntryNotFound = errors.New("catalog entry not found")

func catPIDKey(provider, marketplace, pid string) string {
	return catPIDPrefix + provider + ":" + marketplace + ":" + pid
}

func (s *CatalogStore) get(key string) ([]byte, error) {
	val, closer, err := s.db.Get([]byte(key))
	if errors.Is(err, pebble.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	return append([]byte(nil), val...), nil
}

// UpsertResult reports what one UpsertEntries call did.
type UpsertResult struct {
	Created int
	Updated int
	// IDs is the entry id of each input, in input order.
	IDs []string
}

// UpsertEntries writes entries keyed by (provider, marketplace, provider_id),
// idempotently: an existing entry keeps its id, first_seen_at and
// edition_group_id; its harvested_by gains harvestKey; stale_since clears.
// All inputs commit in one batch.
func (s *CatalogStore) UpsertEntries(items []CatalogUpsert, harvestKey string) (UpsertResult, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	res := UpsertResult{IDs: make([]string, len(items))}
	batch := s.db.NewBatch()
	defer batch.Close()
	now := s.now().UTC()

	// Inputs inside one call can repeat a pid or a group key; these maps
	// make the second occurrence see the first, as a later call would.
	pendingPID := map[string]*CatalogEntry{}
	pendingGroup := map[string]string{}

	for i := range items {
		in := items[i].Entry
		if in.Provider == "" || in.ProviderID == "" || in.Marketplace == "" {
			return UpsertResult{}, fmt.Errorf("catalog upsert: provider, provider_id and marketplace are required (got %q/%q/%q)", in.Provider, in.ProviderID, in.Marketplace)
		}
		pk := catPIDKey(in.Provider, in.Marketplace, in.ProviderID)

		var old *CatalogEntry
		if p, ok := pendingPID[pk]; ok {
			old = p
		} else {
			idRaw, err := s.get(pk)
			if err != nil {
				return UpsertResult{}, fmt.Errorf("catalog upsert: read %s: %w", pk, err)
			}
			if idRaw != nil {
				e, err := s.GetEntry(string(idRaw))
				if err != nil && !errors.Is(err, ErrCatalogEntryNotFound) {
					return UpsertResult{}, err
				}
				old = e
			}
		}

		entry := in
		entry.HarvestedAt = now
		entry.StaleSince = nil
		entry.ConfirmedBy, entry.ConflictBy = nil, nil
		if old != nil {
			entry.ID = old.ID
			entry.FirstSeenAt = old.FirstSeenAt
			// Stable assignment (R6): a group id is never recomputed. If the
			// grouping rules change later, regrouping is an explicit op.
			entry.EditionGroupID = old.EditionGroupID
			entry.EditionGroupKey = old.EditionGroupKey
			entry.HarvestedBy = old.HarvestedBy
			entry.ConfirmedBy, entry.ConflictBy = old.ConfirmedBy, old.ConflictBy
			res.Updated++
		} else {
			entry.ID = ulid.Make().String()
			entry.FirstSeenAt = now
			res.Created++
		}
		if harvestKey != "" && !slices.Contains(entry.HarvestedBy, harvestKey) {
			entry.HarvestedBy = append(slices.Clone(entry.HarvestedBy), harvestKey)
			slices.Sort(entry.HarvestedBy)
		}
		if harvestKey != "" {
			applyVerdict(&entry, harvestKey, !in.NameOnlyAuthor, in.AuthorConflict)
		}

		if entry.EditionGroupID == "" {
			gid, err := s.assignGroup(batch, entry.EditionGroupKey, pendingGroup)
			if err != nil {
				return UpsertResult{}, err
			}
			entry.EditionGroupID = gid
		}
		if entry.EditionKind == "" {
			entry.EditionKind = EditionUnknown
		}

		if old != nil {
			if err := s.deleteIndexes(batch, old); err != nil {
				return UpsertResult{}, err
			}
		}
		data, err := json.Marshal(&entry)
		if err != nil {
			return UpsertResult{}, fmt.Errorf("catalog upsert: marshal %s: %w", entry.ProviderID, err)
		}
		if err := batch.Set([]byte(catEntryPrefix+entry.ID), data, nil); err != nil {
			return UpsertResult{}, err
		}
		if raw := items[i].Raw; len(raw) > 0 {
			if err := batch.Set([]byte(catRawPrefix+entry.ID), raw, nil); err != nil {
				return UpsertResult{}, err
			}
		}
		if err := batch.Set([]byte(pk), []byte(entry.ID), nil); err != nil {
			return UpsertResult{}, err
		}
		if err := s.setIndexes(batch, &entry); err != nil {
			return UpsertResult{}, err
		}
		e := entry
		pendingPID[pk] = &e
		res.IDs[i] = entry.ID
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return UpsertResult{}, fmt.Errorf("catalog upsert: commit: %w", err)
	}
	return res, nil
}

// assignGroup returns the stable group id for key, minting one on first
// sight. An empty key (no author identity or no title) always gets a fresh
// singleton group: R6 forbids grouping on title alone, so there is nothing to
// share it with. Caller holds writeMu.
func (s *CatalogStore) assignGroup(batch *pebble.Batch, key string, pending map[string]string) (string, error) {
	if key == "" {
		return ulid.Make().String(), nil
	}
	if gid, ok := pending[key]; ok {
		return gid, nil
	}
	raw, err := s.get(catGroupKeyPrefix + key)
	if err != nil {
		return "", fmt.Errorf("catalog upsert: read group key: %w", err)
	}
	if raw != nil {
		pending[key] = string(raw)
		return string(raw), nil
	}
	gid := ulid.Make().String()
	if err := batch.Set([]byte(catGroupKeyPrefix+key), []byte(gid), nil); err != nil {
		return "", err
	}
	pending[key] = gid
	return gid, nil
}

func entryIndexKeys(e *CatalogEntry) []string {
	keys := make([]string, 0, len(e.AuthorKeys)+len(e.SeriesKeys)+len(e.HarvestedBy)+3)
	keys = append(keys, catIDPrefix+e.ID)
	for _, k := range e.AuthorKeys {
		keys = append(keys, catAuthorPrefix+k+":"+e.ID)
	}
	for _, k := range e.SeriesKeys {
		keys = append(keys, catSeriesPrefix+k+":"+e.ID)
	}
	for _, k := range e.HarvestedBy {
		keys = append(keys, catHarvestPrefix+k+":"+e.ID)
	}
	if e.EditionGroupID != "" {
		keys = append(keys, catGroupPrefix+e.EditionGroupID+":"+e.ID)
	}
	if e.StaleSince != nil {
		keys = append(keys, catStalePrefix+e.ID)
	}
	return keys
}

func (s *CatalogStore) setIndexes(batch *pebble.Batch, e *CatalogEntry) error {
	for _, k := range entryIndexKeys(e) {
		if err := batch.Set([]byte(k), nil, nil); err != nil {
			return err
		}
	}
	return nil
}

func (s *CatalogStore) deleteIndexes(batch *pebble.Batch, e *CatalogEntry) error {
	for _, k := range entryIndexKeys(e) {
		if err := batch.Delete([]byte(k), nil); err != nil {
			return err
		}
	}
	return nil
}

// MarkUnseen is the stale pass after a COMPLETE, uncapped fetch for
// harvestKey (R10: stale_since only after a complete fetch below the cap; the
// caller enforces that). Every entry filed under harvestKey whose id is not in
// seen loses harvestKey from harvested_by; an entry whose harvested_by
// becomes empty gets stale_since (if not already set). The key's verdicts
// (ConfirmedBy, ConflictBy) go with it in both cases. Entries are never
// deleted. Returns how many entries became stale.
func (s *CatalogStore) MarkUnseen(harvestKey string, seen map[string]bool) (int, error) {
	if harvestKey == "" {
		return 0, errors.New("catalog mark unseen: empty harvest key")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	ids, err := s.idsUnder(catHarvestPrefix + harvestKey + ":")
	if err != nil {
		return 0, err
	}
	batch := s.db.NewBatch()
	defer batch.Close()
	now := s.now().UTC()
	stale := 0
	for _, id := range ids {
		if seen[id] {
			continue
		}
		e, err := s.GetEntry(id)
		if errors.Is(err, ErrCatalogEntryNotFound) {
			// Dangling index row: drop it rather than carry it forever.
			if err := batch.Delete([]byte(catHarvestPrefix+harvestKey+":"+id), nil); err != nil {
				return 0, err
			}
			continue
		}
		if err != nil {
			return 0, err
		}
		if err := s.deleteIndexes(batch, e); err != nil {
			return 0, err
		}
		e.HarvestedBy = slices.DeleteFunc(slices.Clone(e.HarvestedBy), func(k string) bool { return k == harvestKey })
		// A harvester that no longer lists the entry has no verdict on it.
		// That holds when it was the last one too: a stale entry must not
		// keep reading "confirmed by author ASIN" on the strength of a
		// listing that has stopped returning it.
		e.ConfirmedBy = withoutKey(e.ConfirmedBy, harvestKey)
		e.ConflictBy = withoutKey(e.ConflictBy, harvestKey)
		deriveVerdictFlags(e)
		if len(e.HarvestedBy) == 0 && e.StaleSince == nil {
			t := now
			e.StaleSince = &t
			stale++
		}
		data, err := json.Marshal(e)
		if err != nil {
			return 0, err
		}
		if err := batch.Set([]byte(catEntryPrefix+e.ID), data, nil); err != nil {
			return 0, err
		}
		if err := s.setIndexes(batch, e); err != nil {
			return 0, err
		}
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return 0, fmt.Errorf("catalog mark unseen: commit: %w", err)
	}
	return stale, nil
}

// GetEntry returns one entry by id.
func (s *CatalogStore) GetEntry(id string) (*CatalogEntry, error) {
	raw, err := s.get(catEntryPrefix + id)
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, ErrCatalogEntryNotFound
	}
	var e CatalogEntry
	if err := json.Unmarshal(raw, &e); err != nil {
		return nil, fmt.Errorf("catalog entry %s: decode: %w", id, err)
	}
	return &e, nil
}

// GetEntryByProviderID resolves (provider, marketplace, pid) to an entry.
func (s *CatalogStore) GetEntryByProviderID(provider, marketplace, pid string) (*CatalogEntry, error) {
	raw, err := s.get(catPIDKey(provider, marketplace, pid))
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, ErrCatalogEntryNotFound
	}
	return s.GetEntry(string(raw))
}

// GetRaw returns the stored provider payload for an entry (nil if none).
func (s *CatalogStore) GetRaw(id string) ([]byte, error) {
	return s.get(catRawPrefix + id)
}

// idsUnder returns the trailing id component of every key under prefix.
// Only index prefixes (empty values) may be passed: never catEntryPrefix,
// whose values are the ~1 KB entry JSON.
func (s *CatalogStore) idsUnder(prefix string) ([]string, error) {
	ids, _, err := s.pageUnder(prefix, 0, -1)
	return ids, err
}

// pageUnder walks the keys under an index prefix (empty values) and returns
// the trailing ids of keys [offset, offset+limit) plus the total key count.
// limit < 0 means no limit. Ids past the window are counted, never copied.
func (s *CatalogStore) pageUnder(prefix string, offset, limit int) ([]string, int, error) {
	if s.scanHook != nil {
		s.scanHook(prefix)
	}
	p := []byte(prefix)
	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: p, UpperBound: prefixUpperBound(p)})
	if err != nil {
		return nil, 0, err
	}
	defer iter.Close()
	var ids []string
	n := 0
	for iter.First(); iter.Valid(); iter.Next() {
		if n >= offset && (limit < 0 || len(ids) < limit) {
			ids = append(ids, string(bytes.TrimPrefix(iter.Key(), p)))
		}
		n++
	}
	return ids, n, iter.Error()
}

// CatalogListQuery selects entries for the read API. At most one of AuthorKey
// and SeriesKey may be set; neither lists every entry in id order.
type CatalogListQuery struct {
	AuthorKey string
	SeriesKey string
	Limit     int
	Offset    int
}

// ListEntries returns one page of entries and the total matching count. Every
// branch walks a keys-only index (cat_id, cat_author, cat_series), copies only
// the ids inside the requested window, and decodes only those entries; the
// total is a key count. No branch iterates the entry values.
func (s *CatalogStore) ListEntries(q CatalogListQuery) ([]CatalogEntry, int, error) {
	if q.AuthorKey != "" && q.SeriesKey != "" {
		return nil, 0, errors.New("catalog list: author and series filters are exclusive")
	}
	limit := q.Limit
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	offset := max(q.Offset, 0)

	prefix := catIDPrefix
	switch {
	case q.AuthorKey != "":
		prefix = catAuthorPrefix + q.AuthorKey + ":"
	case q.SeriesKey != "":
		prefix = catSeriesPrefix + q.SeriesKey + ":"
	}
	ids, total, err := s.pageUnder(prefix, offset, limit)
	if err != nil {
		return nil, 0, err
	}
	out := make([]CatalogEntry, 0, len(ids))
	for _, id := range ids {
		e, err := s.GetEntry(id)
		if errors.Is(err, ErrCatalogEntryNotFound) {
			continue
		}
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *e)
	}
	return out, total, nil
}

// EditionGroupMembers returns the entry ids in one edition group.
func (s *CatalogStore) EditionGroupMembers(groupID string) ([]string, error) {
	return s.idsUnder(catGroupPrefix + groupID + ":")
}

// CountEntries counts every catalog entry by its keys-only cat_id index.
func (s *CatalogStore) CountEntries() (int, error) {
	_, n, err := s.pageUnder(catIDPrefix, 0, 0)
	return n, err
}

// CountHarvestedBy counts the entries a harvest key currently lists (its
// keys-only cat_hv index): the entries a stale pass for it could touch.
func (s *CatalogStore) CountHarvestedBy(harvestKey string) (int, error) {
	if harvestKey == "" {
		return 0, nil
	}
	_, n, err := s.pageUnder(catHarvestPrefix+harvestKey+":", 0, 0)
	return n, err
}

// CountStale counts entries with stale_since set.
func (s *CatalogStore) CountStale() (int, error) {
	_, n, err := s.pageUnder(catStalePrefix, 0, 0)
	return n, err
}

// GetAuthorState returns the harvest state for key, or nil if none.
func (s *CatalogStore) GetAuthorState(key string) (*CatalogAuthorState, error) {
	raw, err := s.get(catAuthorStatePrefix + key)
	if err != nil || raw == nil {
		return nil, err
	}
	var st CatalogAuthorState
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, fmt.Errorf("catalog author state %s: decode: %w", key, err)
	}
	return &st, nil
}

// PutAuthorState writes the harvest state for st.Key.
func (s *CatalogStore) PutAuthorState(st *CatalogAuthorState) error {
	if st == nil || strings.TrimSpace(st.Key) == "" {
		return errors.New("catalog author state: empty key")
	}
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return s.db.Set([]byte(catAuthorStatePrefix+st.Key), data, pebble.Sync)
}

// CountAuthorStates tallies author states by State.
func (s *CatalogStore) CountAuthorStates() (map[string]int, error) {
	p := []byte(catAuthorStatePrefix)
	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: p, UpperBound: prefixUpperBound(p)})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	out := map[string]int{CatalogHarvestComplete: 0, CatalogHarvestPartial: 0, CatalogHarvestFailed: 0}
	for iter.First(); iter.Valid(); iter.Next() {
		var st CatalogAuthorState
		if err := json.Unmarshal(iter.Value(), &st); err != nil {
			return nil, fmt.Errorf("catalog author state %s: decode: %w", iter.Key(), err)
		}
		out[st.State]++
	}
	return out, iter.Error()
}

// applyVerdict records one harvester's verdict on an entry and re-derives
// the flags. Verdicts from keys no longer in HarvestedBy (an entry revived
// after going stale) are dropped first, so only current harvesters count.
func applyVerdict(e *CatalogEntry, key string, confirmed, conflict bool) {
	current := func(k string) bool { return k != key && slices.Contains(e.HarvestedBy, k) }
	e.ConfirmedBy = slices.DeleteFunc(slices.Clone(e.ConfirmedBy), func(k string) bool { return !current(k) })
	e.ConflictBy = slices.DeleteFunc(slices.Clone(e.ConflictBy), func(k string) bool { return !current(k) })
	if confirmed {
		e.ConfirmedBy = append(e.ConfirmedBy, key)
		slices.Sort(e.ConfirmedBy)
	}
	if conflict {
		e.ConflictBy = append(e.ConflictBy, key)
		slices.Sort(e.ConflictBy)
	}
	deriveVerdictFlags(e)
}

// deriveVerdictFlags: name_only unless some current harvester confirmed the
// entry by author ASIN; conflict if any current harvester saw one.
func deriveVerdictFlags(e *CatalogEntry) {
	if len(e.ConfirmedBy) == 0 {
		e.ConfirmedBy = nil
	}
	if len(e.ConflictBy) == 0 {
		e.ConflictBy = nil
	}
	e.NameOnlyAuthor = len(e.ConfirmedBy) == 0
	e.AuthorConflict = len(e.ConflictBy) > 0
}

func withoutKey(keys []string, key string) []string {
	return slices.DeleteFunc(slices.Clone(keys), func(k string) bool { return k == key })
}
