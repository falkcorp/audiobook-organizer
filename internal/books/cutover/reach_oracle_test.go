// file: internal/books/cutover/reach_oracle_test.go
// version: 1.6.0
// guid: c27d5a40-9e18-4b63-8f02-3a6d1e7b9c54
// last-edited: 2026-11-15

package cutover

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m3"
	"github.com/falkcorp/audiobook-organizer/internal/server"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/storage"
)

// The reach oracle (F4-07): what every principal of 05-rbac section 12 may
// reach on R0, recorded from main at the commit r0config/r0_reach_commit.txt
// pins, built by scripts/r2corpus/build_r0_reach.py (internal/books/rbacreach,
// scripts/r2corpus/record_r0_reach.py; F4-08 moved it off the v0.223.0 release
// asset), against what the same principals reach
// after M3 on the migrated corpus, under the manifest's route requirements
// resolved through the atom table.
//
// What remains here is the recording's types and loader, the seed of the migrated
// corpus and the manifest. The hand evaluator that judged the manifest was replaced
// by registered_reach_eval_test.go (F4-08 B10f), which sends the recording to the
// flipped build through httpapi.Authorize. See
// docs/agent-tasks/platform-core/seams.d/F4-07.md.

const reachDir = "internal/books/rbacreach"

// ---- the recorded R0 answers ----

type reachPrincipal struct {
	Name   string   `json:"name"`
	Kind   string   `json:"kind"` // session, key, anonymous, authoff
	User   string   `json:"user"`
	Role   string   `json:"role"`
	Scopes []string `json:"scopes"`
}

type reachPrincipalsDoc struct {
	Principals  []reachPrincipal           `json:"principals"`
	Users       map[string]string          `json:"users"`        // username -> role label the recorder gave it
	CustomRoles map[string]reachCustomRole `json:"custom_roles"` // role label -> the custom role's title and HEAD atoms
}

type reachR0Route struct {
	Method  string `json:"method"`
	Path    string `json:"path"`
	ABSOnly bool   `json:"abs_only"`
	Status  []int  `json:"status"`
	Verdict string `json:"verdict"` // one letter per principal, in Principals' order: A allow, D deny
	// AdminRefusal is the admin session's refusal text on an owner-set route: R0's
	// own gate message, recorded so a 403 for another reason is not read as the gate.
	AdminRefusal string `json:"admin_refusal"`
	// Reason is, per principal, the closed-vocabulary token for the cause of a 5xx
	// cell ("" for every other cell). Only routes with at least one 5xx cell carry it.
	Reason []string `json:"reason"`
}

func (r reachR0Route) key() string { return r.Method + " " + r.Path }

type reachR0Task struct {
	ID          string   `json:"id"`
	Permissions []string `json:"permissions"`
	Approver    string   `json:"approver"`
	Verdict     string   `json:"verdict"`
}

type reachR0Approval struct {
	RunID   string         `json:"run_id"`
	Run     map[string]any `json:"run"`
	Verdict string         `json:"verdict"` // A, D, or N (the probe did not reach the approver check)
}

// reachR0OwnerSet is R0's answers on the owner-gated routes with owner_email set
// in the stored config and no Cloudflare Access verifier, so no principal of the
// table is the owner (record_r0_reach.py owner_set).
type reachR0OwnerSet struct {
	What       string         `json:"what"`
	OwnerEmail string         `json:"owner_email"`
	Canary     map[string]int `json:"canary"` // each principal's canary status in the owner boots
	Routes     []reachR0Route `json:"routes"`
}

type reachR0Doc struct {
	R0Commit        string            `json:"r0_commit"`      // the main commit R0 was built from
	OverlaySHA256   string            `json:"overlay_sha256"` // digest of the overlay store the recording copied
	Recorded        map[string]string `json:"recorded"`       // the run that wrote it (provenance, never compared)
	RouteCount      int               `json:"route_count"`
	Principals      []string          `json:"principals"`
	Routes          []reachR0Route    `json:"routes"`
	UniformNotFound []string          `json:"uniform_not_found"`
	Tasks           []reachR0Task     `json:"tasks"`
	Approvals       []reachR0Approval `json:"approvals"`
	OwnerSet        *reachR0OwnerSet  `json:"owner_set"` // nil until R0 is recorded with an owner set
	Canary          map[string]int    `json:"canary"`    // each principal's canary status in the main boots
}

func reachReadJSON(t testing.TB, name string, v any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), filepath.FromSlash(reachDir), name))
	if err != nil {
		t.Fatalf("the recorded R0 answers: %v (regenerate with scripts/r2corpus/record_r0_reach.py)", err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

func reachLoad(t testing.TB) (*reachPrincipalsDoc, *reachR0Doc) {
	t.Helper()
	var pd reachPrincipalsDoc
	var vd reachR0Doc
	reachReadJSON(t, "principals.json", &pd)
	reachReadJSON(t, "verdicts.json", &vd)
	names := make([]string, len(pd.Principals))
	for i, p := range pd.Principals {
		names[i] = p.Name
	}
	if len(vd.R0Commit) != 40 {
		t.Fatalf("verdicts.json: r0_commit %q is not a full commit SHA (record_r0_reach.py writes it)", vd.R0Commit)
	}
	if !slices.Equal(names, vd.Principals) {
		t.Fatalf("principals.json and verdicts.json list different principals:\n%v\n%v", names, vd.Principals)
	}
	for _, r := range vd.Routes {
		if len(r.Verdict) != len(names) {
			t.Fatalf("%s: %d verdict cells for %d principals", r.key(), len(r.Verdict), len(names))
		}
	}
	if vd.RouteCount != len(vd.Routes) {
		t.Fatalf("verdicts.json says %d routes and holds %d", vd.RouteCount, len(vd.Routes))
	}
	return &pd, &vd
}

// ---- seeding the principals into the migrated corpus ----

// reachKeyID is the deterministic HEAD key ID of the i-th key principal, a
// valid ULID the corpus has no row for.
func reachKeyID(i int) string { return fmt.Sprintf("01M7R2%020d", i) }

func reachKeyRaw(i int) string { return fmt.Sprintf("reach-oracle-fixture-%02d", i) }

// reachSeed is the overlay store plus one HEAD key row per key principal,
// owned by the principal's user. keyID maps a principal name to its key's ID;
// owner maps a username to the user ID the corpus holds.
type reachSeed struct {
	keyID map[string]string
	owner map[string]string
}

func (s *reachSeed) option(principals []reachPrincipal) func(*idRig) {
	return func(r *idRig) {
		prev := r.mutate
		r.mutate = func(t testing.TB, lr storage.LegacyRange) {
			if prev != nil {
				prev(t, lr)
			}
			ctx := context.Background()
			s.keyID, s.owner = map[string]string{}, map[string]string{}
			var rows []seedRow
			for i, p := range principals {
				if p.Kind != "key" {
					continue
				}
				if _, ok := s.owner[p.User]; !ok {
					raw, err := lr.Get(ctx, []byte("idx:user:username:"+strings.ToLower(p.User)))
					if err != nil || len(raw) == 0 {
						t.Fatalf("the overlay has no user %q: %v", p.User, err)
					}
					s.owner[p.User] = string(raw)
				}
				id := reachKeyID(i)
				s.keyID[p.Name] = id
				rows = append(rows, headKeyRows(id, s.owner[p.User], reachKeyRaw(i), p.Scopes, "active")...)
			}
			tmp := &idRig{}
			idRows(rows...)(tmp)
			tmp.mutate(t, lr)
		}
	}
}

// reachManifest is books' ABS-on route manifest, the one the route dump covers. The
// registered-path oracle (registered_reach_eval_test.go) judges what the flip
// serves; the obligation bars, the pin checks and the route-set pins read this.
func reachManifest() []httpapi.LegacyRoute { return server.LegacyRoutes(true) }

// ---- running the oracle ----

// reachMigrate runs M3 over the seeded overlay with atoms as the table it
// migrates by, and returns what it left. reach_roles_test.go holds what M3 left
// to the principal table's roles.
func reachMigrate(t testing.TB, pd *reachPrincipalsDoc, atoms []m3.AtomRow) (*idSnap, *reachSeed) {
	t.Helper()
	seed := &reachSeed{}
	rig := newIDRig(t, "overlay", seed.option(pd.Principals), func(r *idRig) {
		r.config = func(c *m3.Config) {
			c.Atoms, c.SeedRoles, c.Catalog, c.Ambient = atoms, reachSeedRoles(), reachCatalog(t), reachAmbient()
		}
	})
	snap, err := rig.run(t)
	if err != nil {
		t.Fatalf("M3 over the overlay corpus: %v", err)
	}
	if snap.State != "done" {
		t.Fatalf("M3 left the cutover in state %q, want done", snap.State)
	}
	return snap, seed
}
