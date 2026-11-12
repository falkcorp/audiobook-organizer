// file: internal/books/cutover/identity_fixtures_test.go
// version: 1.0.0
// guid: a05fe568-4b4a-4127-916c-00be775fce56
// last-edited: 2026-11-12

package cutover

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// Corpus IDs the M3 tests name (internal/books/testdata/r2corpus/manifest.json).
// Every person is a Batman character; every credential a generated fixture.
const (
	uAdmin        = "01M72KQHXM3TJDMDBTC8G90416" // admin (HEAD's admin seed)
	uDick         = "01M72KQJFNXWXQMGSX75S0JC6H" // dick.grayson (editor)
	uBarbara      = "01M72KQJJ3QTYQ3XCF5F2Y7GS3" // barbara.gordon (viewer)
	uWorker       = "01M72KQJQEJ61RCZ4EQ9C64QJN" // fp.worker (fp-worker)
	uSystem       = "01M72KQH0DNEW356YZS9YA1EVD" // _system (status "system")
	uJamesGordon  = "01M731PAXHJA74MGPRM4RC9T2G" // custom role: every atom
	uHarleen      = "01M731PB206E61WD4FWY6NHZ1C" // custom role: a subset
	uLuciusJr     = "01M731PB6AZAWZKSYQ5SYQ1VC3" // custom role: one unknown atom
	uTalia        = "01M731PB8ZQDGMQ4DQP9EN9KTH" // viewer + a role whose every atom is unknown
	uUbu          = "01M731PBB982454BR3F9C27EXS" // only that role
	roleEvery     = "01M731PAT6GSYY06M5E1FCD9GY"
	roleSubset    = "01M731PAXVV5KY3QJ39HNH4T6P"
	roleUnknown   = "01M731PB29NMM8BSJ4YWEP5Y6S"
	roleAllUnk    = "01M731PB6K05668MX7XJZ7Z3RV"
	roleCorrupt   = "01M731PBBK3AYXT3G3JY7DQAYF"
	kBootstrap    = "01M72KQHY609X3XA43201NNBN5" // revoked, every atom, admin-owned
	kScopedView   = "01M72KQK4PT3EY30BZCQ9W6CMT" // [library.view], admin-owned
	kEmptyAdmin   = "01M72KQWY3JTKBCXKZD6EWMCC1" // [] , admin-owned
	kEveryAdmin   = "01M72KQX1060J3V6FFS7YM43QV" // every atom, admin-owned
	kEditorOwn    = "01M72KQX1NYDQT302YXVR0GJ1V" // [library.view playlists.create], editor-owned
	kWorker       = "01M72KQX25547VWRSM74HTTRYW" // [fingerprint.worker], worker-owned
	kOutsideOwner = "01M731PBC8MYZ7S5856HVGDC6R" // atoms outside the owner's roles
	kEveryCustom  = "01M731PBCK236V8E8ERQ6AH9QS" // every atom, owner holds a custom every-atom role
	kEmptyCustom  = "01M731PBCWCRJSHHS4BPA0HV0W" // [], owner holds a custom subset role
	kPadActive    = "01M731PBV8M9RPKZN49PVFS0P2"
	kUpperInact   = "01M731PBVKXDBRCVG99NR26ZR1"
	kEmptyStatus  = "01M731PBVZD9RCX18G36G6TA5N"
	kPadRevoked   = "01M731PBWBWXCW3FC1Z9P0D546"
	kRevokedAt    = "01M731PBWQAEVJVPV1QXEBD3QF"
	kPaused       = "01M731PBX2F186GASP86YP48VB"
	kNoExpiry     = "01M731PBXDTTCRJFNXNHA8H1ZH"
)

// The blocking rows of F2-13's blocker stores (users 1 and 2 are the corrupt
// and the importer-rejected user; rows A to D the corrupt key, the corrupt
// identity, the orphan key and the orphan identity). Neutral names: a
// secret scanner reads "key" next to a ULID as a credential.
const (
	bUser1 = "01M731PCWW8140RBE27AA0P5M9"
	bRowA  = "01M731PDF3NCXWBNDWNEYDVNAY"
	bRowB  = "01M731PE1AJHC4E6M3SCW38XN7"
	bUser2 = "01M731PEQXKMNAPEJCT6GF1BJK"
	bRowC  = "01M731PFAD0FPTETYSW6E7QDGZ"
	bRowD  = "01M731PFWKXXBAC5TD8D26JVFH"
)

// A password hash and a token digest no real credential has.
const idArgon = "$argon2id$v=19$m=65536,t=3,p=4$YmF0Y2F2ZS1zYWx0$YmF0bW9iaWxlLXRlc3QtaGFzaA"

func idDigest(raw string) string { s := sha256.Sum256([]byte(raw)); return hex.EncodeToString(s[:]) }

// seedRow is one legacy row a test adds (or, with a nil value, removes).
type seedRow struct {
	key string
	val any // a JSON value, a []byte or string written verbatim, or nil to delete
}

// idRows adds rows to what r.mutate seeds, after the corpus.
func idRows(rows ...seedRow) func(*idRig) {
	return func(r *idRig) {
		prev := r.mutate
		r.mutate = func(t testing.TB, lr storage.LegacyRange) {
			if prev != nil {
				prev(t, lr)
			}
			ctx := context.Background()
			for _, row := range rows {
				var err error
				switch v := row.val.(type) {
				case nil:
					err = lr.Delete(ctx, []byte(row.key))
				case []byte:
					err = lr.Set(ctx, []byte(row.key), v)
				case string:
					err = lr.Set(ctx, []byte(row.key), []byte(v))
				default:
					raw, merr := json.Marshal(v)
					if merr != nil {
						t.Fatal(merr)
					}
					err = lr.Set(ctx, []byte(row.key), raw)
				}
				if err != nil {
					t.Fatalf("seed %s: %v", row.key, err)
				}
			}
		}
	}
}

// headUserRows is a HEAD user with its username index row.
func headUserRows(id, username string, roles []string, status string) []seedRow {
	return []seedRow{
		{"u:" + id, map[string]any{"id": id, "username": username, "email": "", "password_hash_algo": "argon2id", "password_hash": idArgon,
			"roles": roles, "status": status, "created_at": "2026-11-10T11:00:00Z", "updated_at": "2026-11-10T11:00:00Z", "version": 1}},
		{"idx:user:username:" + strings.ToLower(username), id},
	}
}

// headKeyRows is a HEAD API key with its hash and by-user index rows. raw is
// the key's raw value; the row holds its SHA-256.
func headKeyRows(id, owner, raw string, scopes []string, status string) []seedRow {
	h := idDigest(raw)
	return []seedRow{
		{"apikey:" + id, map[string]any{"id": id, "user_id": owner, "name": "key-" + id[len(id)-4:], "token_hash": h, "scopes": scopes,
			"status": status, "created_at": "2026-11-10T11:00:00Z", "expires_at": "2027-11-10T11:00:00Z", "use_count": 0}},
		{"idx:apikey:hash:" + h, id},
		{"idx:apikey:user:" + owner + ":" + id, id},
	}
}

func rowsOf(groups ...[]seedRow) []seedRow {
	var out []seedRow
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
}

// ---- reading what M3 left ----

func scopeStrings(ps []rbac.Permission) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = string(p)
	}
	slices.Sort(out)
	return out
}

func sortedStrings(in ...string) []string { out := slices.Clone(in); slices.Sort(out); return out }

// legacyIDs lists the IDs of a HEAD family's rows in a legacy dump.
func legacyIDs(legacy map[string][]byte, prefix string) []string {
	var out []string
	for k := range legacy {
		if id, ok := strings.CutPrefix(k, prefix); ok {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

func mapKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// everyScope is every native scope of every atom row, plus core:admin first.
func everyScope() []string {
	var out []string
	for _, a := range idAtoms() {
		for _, s := range a.Scopes {
			out = append(out, string(s))
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func withAmbientScopes(scopes ...string) []string {
	out := append(slices.Clone(scopes), scopeStrings(idAmbient())...)
	slices.Sort(out)
	return slices.Compact(out)
}

func nativeRoleName(id string) rbac.RoleName {
	return rbac.RoleName("custom.role-" + strings.ToLower(id))
}

var _ = []any{time.Now, auth.StatusActive}
