// file: internal/server/handlers/entities/author_ref_audit_test.go
// version: 1.0.0
// guid: 0b6f2e91-4a7c-4d18-9f52-6c8a3d0e7b41
// last-edited: 2026-08-29

package entities_test

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/entities"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The audit endpoint is only meaningful against a store whose GetAuthorByID
// FOLLOWS the tombstone redirect, because that is the behaviour the whole
// tombstone-first ordering exists to work around. A hand-written mock that
// returns (nil, nil) for a tombstoned id does NOT have that shape, and a test
// built on one cannot observe an ordering bug at all. So the classification
// tests below run against a real PebbleStore.
var _ entities.EntitiesStore = (*database.PebbleStore)(nil)

// refAuditFixture is a real store carrying one author of every classification
// the endpoint distinguishes, plus the ids needed to assert on them.
type refAuditFixture struct {
	store *database.PebbleStore

	liveID   int
	liveName string

	// canonicalID is a LIVE author that mergedID's tombstone redirects to.
	canonicalID   int
	canonicalName string
	// mergedID is deleted with a tombstone pointing at canonicalID: the
	// self-healing case that must NOT be counted as damage.
	mergedID int

	// danglingID is deleted with NO tombstone: the genuinely broken case.
	danglingID int

	// orphanTombID is deleted with a tombstone pointing at deadTargetID, which
	// is itself deleted. The chain does not heal, and the response must show
	// that by naming no target.
	orphanTombID int
	deadTargetID int
}

func newRefAuditFixture(t *testing.T) *refAuditFixture {
	t.Helper()
	store, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	mk := func(name string) int {
		a, err := store.CreateAuthor(name)
		require.NoError(t, err)
		require.NotNil(t, a)
		return a.ID
	}

	f := &refAuditFixture{
		store:         store,
		liveName:      "Live Author",
		canonicalName: "Canonical Author",
	}
	f.liveID = mk(f.liveName)
	f.canonicalID = mk(f.canonicalName)
	f.mergedID = mk("Merged Away Author")
	f.danglingID = mk("Deleted Without Tombstone")
	f.deadTargetID = mk("Dead Redirect Target")
	f.orphanTombID = mk("Redirects To A Dead Author")

	require.NoError(t, store.DeleteAuthor(f.mergedID))
	require.NoError(t, store.CreateAuthorTombstone(f.mergedID, f.canonicalID))

	require.NoError(t, store.DeleteAuthor(f.danglingID))

	require.NoError(t, store.DeleteAuthor(f.deadTargetID))
	require.NoError(t, store.DeleteAuthor(f.orphanTombID))
	require.NoError(t, store.CreateAuthorTombstone(f.orphanTombID, f.deadTargetID))

	return f
}

// TestRefAuditFixtureHasProductionErrorShape pins the store behaviour every
// assertion below depends on. If GetAuthorByID ever stops following the
// redirect, or starts reporting a missing author as an error instead of
// (nil, nil), the classification tests would silently stop testing what they
// claim to and this test says so directly.
func TestRefAuditFixtureHasProductionErrorShape(t *testing.T) {
	f := newRefAuditFixture(t)

	// A tombstoned id RESOLVES, to the canonical author — which is exactly why
	// GetAuthorByID cannot tell live from tombstoned and the tombstone has to
	// be consulted first.
	redirected, err := f.store.GetAuthorByID(f.mergedID)
	require.NoError(t, err)
	require.NotNil(t, redirected)
	assert.Equal(t, f.canonicalID, redirected.ID)
	assert.Equal(t, f.canonicalName, redirected.Name)

	// A genuinely absent author is (nil, nil) — NOT a wrapped not-found error.
	missing, err := f.store.GetAuthorByID(f.danglingID)
	require.NoError(t, err)
	assert.Nil(t, missing)

	// And an id with no tombstone is (0, nil).
	canonical, err := f.store.GetAuthorTombstone(f.liveID)
	require.NoError(t, err)
	assert.Zero(t, canonical)
}

// refAuditBucket mirrors the handler's private bucket type for decoding.
type refAuditBucket struct {
	ID          int    `json:"id"`
	CanonicalID int    `json:"canonical_id"`
	Name        string `json:"name"`
}

type refAuditPayload struct {
	Live       []refAuditBucket `json:"live"`
	Tombstoned []refAuditBucket `json:"tombstoned"`
	Dangling   []int            `json:"dangling"`
	Counts     map[string]int   `json:"counts"`
	Requested  int              `json:"requested"`
	Deduped    int              `json:"deduped"`
}

// decodeRefAudit pulls the audit payload out of the {"data": ...} envelope.
func decodeRefAudit(t *testing.T, body []byte) refAuditPayload {
	t.Helper()
	var envelope struct {
		Data refAuditPayload `json:"data"`
	}
	require.NoError(t, json.Unmarshal(body, &envelope))
	return envelope.Data
}

func joinIDs(ids ...int) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.Itoa(id)
	}
	return strings.Join(parts, ",")
}

func auditRequest(t *testing.T, store entities.EntitiesStore, ids string) (int, []byte) {
	t.Helper()
	h := entities.New(store, nil, nil, nil, nil, nil, nil, nil)
	c, w := newCtx(http.MethodGet, "/authors/ref-audit?ids="+ids, "", nil)
	h.AuditAuthorRefs(c)
	return w.Code, w.Body.Bytes()
}

// TestAuditAuthorRefs_SeparatesDanglingFromSelfHealing is the endpoint's whole
// reason for existing: of four broken-looking references, only ONE is repair
// scope.
func TestAuditAuthorRefs_SeparatesDanglingFromSelfHealing(t *testing.T) {
	f := newRefAuditFixture(t)

	code, body := auditRequest(t, f.store,
		joinIDs(f.liveID, f.mergedID, f.danglingID, f.orphanTombID))
	require.Equal(t, http.StatusOK, code)
	got := decodeRefAudit(t, body)

	// Live: only the author that still has a row of its own. A tombstoned id
	// resolves through GetAuthorByID too, so this is the assertion that fails
	// if the tombstone check ever stops running first.
	assert.Equal(t, []refAuditBucket{{ID: f.liveID, Name: f.liveName}}, got.Live)

	assert.Equal(t, []refAuditBucket{
		// Self-healing: names its live redirect target.
		{ID: f.mergedID, CanonicalID: f.canonicalID, Name: f.canonicalName},
		// Redirect target is itself gone, so no name — the tombstone is
		// present but does not actually heal anything.
		{ID: f.orphanTombID, CanonicalID: f.deadTargetID, Name: ""},
	}, got.Tombstoned)

	assert.Equal(t, []int{f.danglingID}, got.Dangling)
	assert.Equal(t, map[string]int{"live": 1, "tombstoned": 2, "dangling": 1}, got.Counts)
}

// TestAuditAuthorRefs_FindsNothingWhenEverythingIsLive is the negative case: a
// healthy library must report an EMPTY dangling bucket, serialized as [] rather
// than null so a caller can length-check it without a nil dance.
func TestAuditAuthorRefs_FindsNothingWhenEverythingIsLive(t *testing.T) {
	f := newRefAuditFixture(t)

	code, body := auditRequest(t, f.store, joinIDs(f.liveID, f.canonicalID))
	require.Equal(t, http.StatusOK, code)

	raw := string(body)
	assert.Contains(t, raw, `"dangling":[]`, "empty dangling bucket must serialize as [], not null")
	assert.Contains(t, raw, `"tombstoned":[]`, "empty tombstoned bucket must serialize as [], not null")

	got := decodeRefAudit(t, body)
	assert.Empty(t, got.Dangling)
	assert.Empty(t, got.Tombstoned)
	assert.Equal(t, []refAuditBucket{
		{ID: f.liveID, Name: f.liveName},
		{ID: f.canonicalID, Name: f.canonicalName},
	}, got.Live)
	assert.Equal(t, map[string]int{"live": 2, "tombstoned": 0, "dangling": 0}, got.Counts)
}

// TestAuditAuthorRefs_AllDangling is the mirror image: every id is broken, and
// the live/tombstoned buckets must come back empty rather than null.
func TestAuditAuthorRefs_AllDangling(t *testing.T) {
	f := newRefAuditFixture(t)

	code, body := auditRequest(t, f.store, joinIDs(f.danglingID, f.deadTargetID))
	require.Equal(t, http.StatusOK, code)

	assert.Contains(t, string(body), `"live":[]`, "empty live bucket must serialize as [], not null")

	got := decodeRefAudit(t, body)
	assert.Equal(t, []int{f.danglingID, f.deadTargetID}, got.Dangling)
	assert.Empty(t, got.Live)
	assert.Equal(t, map[string]int{"live": 0, "tombstoned": 0, "dangling": 2}, got.Counts)
}

// TestAuditAuthorRefs_DedupesButReportsBothCounts: `requested` counts the raw
// comma-separated parts, `deduped` the ids actually looked up. Conflating them
// would hide how much of a caller's list was redundant.
func TestAuditAuthorRefs_DedupesButReportsBothCounts(t *testing.T) {
	f := newRefAuditFixture(t)

	ids := joinIDs(f.liveID, f.liveID, f.danglingID, f.danglingID)
	code, body := auditRequest(t, f.store, ids)
	require.Equal(t, http.StatusOK, code)
	got := decodeRefAudit(t, body)

	assert.Equal(t, 4, got.Requested, "requested counts the raw parts")
	assert.Equal(t, 2, got.Deduped, "deduped counts the distinct ids looked up")
	// A duplicate must not be classified twice.
	assert.Equal(t, []refAuditBucket{{ID: f.liveID, Name: f.liveName}}, got.Live)
	assert.Equal(t, []int{f.danglingID}, got.Dangling)
	assert.Equal(t, map[string]int{"live": 1, "tombstoned": 0, "dangling": 1}, got.Counts)
}

// TestAuditAuthorRefs_PreservesRequestOrder: buckets follow the order the
// caller listed the ids in, so an operator can line the response up against
// their input.
func TestAuditAuthorRefs_PreservesRequestOrder(t *testing.T) {
	f := newRefAuditFixture(t)

	code, body := auditRequest(t, f.store, joinIDs(f.canonicalID, f.liveID))
	require.Equal(t, http.StatusOK, code)
	got := decodeRefAudit(t, body)

	assert.Equal(t, []refAuditBucket{
		{ID: f.canonicalID, Name: f.canonicalName},
		{ID: f.liveID, Name: f.liveName},
	}, got.Live)
}

// TestAuditAuthorRefs_SkipsBlankParts: an id list with stray separators still
// audits the ids it does contain, and `requested` still reflects what was sent.
func TestAuditAuthorRefs_SkipsBlankParts(t *testing.T) {
	f := newRefAuditFixture(t)

	code, body := auditRequest(t, f.store, joinIDs(f.liveID)+",,+,"+joinIDs(f.danglingID))
	require.Equal(t, http.StatusOK, code)
	got := decodeRefAudit(t, body)

	assert.Equal(t, 4, got.Requested)
	assert.Equal(t, 2, got.Deduped)
	assert.Equal(t, []refAuditBucket{{ID: f.liveID, Name: f.liveName}}, got.Live)
	assert.Equal(t, []int{f.danglingID}, got.Dangling)
}

// ── Rejected inputs ────────────────────────────────────────────────────────

func TestAuditAuthorRefs_RejectsBadInput(t *testing.T) {
	f := newRefAuditFixture(t)

	tests := []struct {
		name string
		ids  string
	}{
		{"missing ids", ""},
		{"whitespace only", "%20%20"},
		{"separators only", ",,,"},
		{"non-numeric id", "1,not-a-number,3"},
		{"float id", "1,2.5"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, _ := auditRequest(t, f.store, tc.ids)
			assert.Equal(t, http.StatusBadRequest, code)
		})
	}
}

// TestAuditAuthorRefs_CapsIDCount: the cap is what stops one URL from
// triggering an unbounded number of store lookups. 5000 is allowed, 5001 is
// not — asserting both sides so the boundary cannot drift unnoticed.
func TestAuditAuthorRefs_CapsIDCount(t *testing.T) {
	f := newRefAuditFixture(t)

	atCap := make([]int, 5000)
	for i := range atCap {
		// All the same id: deduped down to one lookup, so this exercises the
		// cap rather than the store.
		atCap[i] = f.liveID
	}
	code, body := auditRequest(t, f.store, joinIDs(atCap...))
	require.Equal(t, http.StatusOK, code, "5000 ids is at the cap and must be accepted")
	assert.Equal(t, 5000, decodeRefAudit(t, body).Requested)

	overCap := make([]int, 0, len(atCap)+1)
	overCap = append(overCap, atCap...)
	overCap = append(overCap, f.liveID)
	code, _ = auditRequest(t, f.store, joinIDs(overCap...))
	assert.Equal(t, http.StatusBadRequest, code, "5001 ids is over the cap and must be rejected")
}

// ── Failure paths the real store cannot produce ────────────────────────────

func TestAuditAuthorRefs_NilStore(t *testing.T) {
	code, _ := auditRequest(t, nil, "1")
	assert.Equal(t, http.StatusInternalServerError, code)
}

// A classification failure must SURFACE, not be silently bucketed. Both store
// calls are error-injected separately because they sit on either side of the
// tombstone branch and a swallowed error on either one would report a healthy
// library.
func TestAuditAuthorRefs_TombstoneLookupError(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetAuthorTombstone(7).Return(0, errString("pebble exploded"))
	c, w := newCtx(http.MethodGet, "/authors/ref-audit?ids=7", "", nil)
	h.AuditAuthorRefs(c)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.NotContains(t, w.Body.String(), `"dangling"`, "a lookup failure must not be reported as a clean audit")
}

func TestAuditAuthorRefs_AuthorLookupError(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetAuthorTombstone(7).Return(0, nil)
	d.store.EXPECT().GetAuthorByID(7).Return(nil, errString("pebble exploded"))
	c, w := newCtx(http.MethodGet, "/authors/ref-audit?ids=7", "", nil)
	h.AuditAuthorRefs(c)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.NotContains(t, w.Body.String(), `"dangling"`, "a lookup failure must not be reported as a clean audit")
}
