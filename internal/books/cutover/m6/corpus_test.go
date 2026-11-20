// file: internal/books/cutover/m6/corpus_test.go
// version: 1.0.3
// guid: 3f9e1b64-2c07-4a85-8d13-b6e0a7c45d92
// last-edited: 2026-11-19

package m6_test

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m6"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
)

// The R2 corpus runs: M6 over the stage1 store R0 wrote and over the overlay
// that adds F2-13's M6 cases, with expected outputs recorded under
// testdata/r2corpus/m6/. Regenerate them with UPDATE_M6_GOLDEN=1; a diff in the
// recorded files is a behaviour change to review, never to accept blind.
//
// M2 is not run here: its harness is the real config and secrets stores with a
// keyring, a HEAD settings key and a gap-fill file, and M6's input is only what
// M2 leaves behind. m2Residual stands in for that, built from the rename table
// M2 itself reads, so a field M2 would keep is kept and a secret M2 would move
// out is moved out. The rest of M2's work (staging the class-k rows, the
// quarantine of the unmapped members) is not M6's input and is not simulated.

const goldenDir = "internal/books/testdata/r2corpus/m6"

// classInputMembers are the settings-blob members the rename table marks class i
// (an input of a later step: they stay in the blob for M6).
func classInputMembers() []string {
	set := map[string]bool{}
	for _, row := range cutover.Table().Rows {
		if row.Stored == cutover.StoredBlob && row.Class == cutover.ClassInput && row.BlobField != "" {
			set[row.BlobField] = true
		}
	}
	return slices.Sorted(maps.Keys(set))
}

// TestCorpus_ClassInputMembersArePinned pins the stand-in's member list to the
// ten members M6 consumes: a rename-table change that adds a class-i member M6
// does not read fails here instead of silently quarantining it.
func TestCorpus_ClassInputMembersArePinned(t *testing.T) {
	want := []string{"ai_endpoints", "deluge_web_url", "download_client", "itunes", "metadata_sources",
		"openai_base_url", "path_aliases", "plugins", "protected_paths", "root_dir"}
	if got := classInputMembers(); !slices.Equal(got, want) {
		t.Fatalf("class-i blob members %v, want %v", got, want)
	}
}

// m2Residual is the blob as M2 leaves it, and the secrets M2 moves out of it,
// from M2's own planner (cutover.PlanBlobResidual runs buildPlan, the code M2's
// step applies): the class-i members stay, every credential, the Deluge daemon
// password and the webhook secret go to the secrets store under the names M2
// gives them, and a member M2 quarantines is gone. Run on the stand-in's own
// rules this was a copy of M2's naming, and it spelled a credential key
// "apiKey" where M2 writes api_key, a name the secrets store refuses.
func m2Residual(t testing.TB, raw []byte) ([]byte, map[secrets.Name]string) {
	t.Helper()
	res, err := cutover.PlanBlobResidual(raw)
	if err != nil {
		t.Fatalf("M2's planner over the corpus blob: %v", err)
	}
	return res.Row, res.Secrets
}

// headSecretRows are HEAD's encrypted setting rows M2 moves into the secrets
// store (class s, stored as a row), with the name each lands under: read from
// the rename table M2's planner walks, not typed here.
func headSecretRows(t testing.TB) map[string]secrets.Name {
	t.Helper()
	out := map[string]secrets.Name{}
	for _, row := range cutover.Table().Rows {
		if row.Top && row.Class == cutover.ClassSecret && row.Stored == cutover.StoredRow {
			out["setting:"+row.Head] = secrets.Name(row.Targets[0].Path)
		}
	}
	if len(out) != 4 {
		t.Fatalf("the rename table has %d top-level secret rows, the stand-in expects HEAD's four encrypted settings", len(out))
	}
	return out
}

// TestRig_SecretsDoubleRefusesWhatTheStoreWould: the double holds a name to
// secrets.Name.Validate on both paths into it (an import and a test's seed), so a
// stand-in that spells a name the store cannot hold fails instead of recording it.
func TestRig_SecretsDoubleRefusesWhatTheStoreWould(t *testing.T) {
	bad := secrets.Name("integrations.metadata.hardcover.credentials.apiKey")
	s := newSecrets()
	if err := s.ImportSecret(context.Background(), nil, secrets.Write{Name: bad, Value: secrets.NewValue("x"), By: "test"}); err == nil {
		t.Error("an import under an invalid name was accepted")
	}
	s.seed[bad] = "x"
	if _, err := s.names(context.Background()); err == nil {
		t.Error("a seeded invalid name was listed")
	}
}

// TestCorpus_M2NamesTheCredentialsInSnakeCase: M2's planner writes a metadata
// source's camel-case credential key as a snake-case secret name, and M6 refers
// to it under that name. This is the name F7-09's M2-then-M6 boot produces.
func TestCorpus_M2NamesTheCredentialsInSnakeCase(t *testing.T) {
	tree := map[string]any{"metadata_sources": []any{
		map[string]any{"id": "hardcover", "name": "Hardcover", "enabled": true, "credentials": map[string]any{"apiKey": "fixture-token-robin"}},
	}}
	res, err := cutover.PlanBlobResidual(blobRow(t, tree))
	if err != nil {
		t.Fatal(err)
	}
	want := secrets.Name("integrations.metadata.hardcover.credentials.api_key")
	if res.Secrets[want] != "fixture-token-robin" || len(res.Secrets) != 1 {
		t.Errorf("M2 imports %v, want exactly %s", res.Secrets, want)
	}
	for n := range res.Secrets {
		if err := n.Validate(); err != nil {
			t.Errorf("M2 imports an invalid name: %v", err)
		}
	}
	if strings.Contains(string(res.Row), "fixture-token-robin") {
		t.Error("the residual blob still holds the credential")
	}
}

// afterM2 shapes a corpus rig as the store M2 leaves: the residual blob, and the
// secrets M2 stored (the fixture's own set is replaced, not added to).
func afterM2(r *rig) {
	clear(r.secrets.seed)
	prev := r.mutate
	r.mutate = func(ctx context.Context, t testing.TB, lr storage.LegacyRange) {
		if prev != nil {
			prev(ctx, t, lr)
		}
		raw, err := lr.Get(ctx, []byte(m6.KeyBlob))
		if err != nil {
			t.Fatalf("the corpus has no settings blob: %v", err)
		}
		residual, stored := m2Residual(t, raw)
		if err := lr.Set(ctx, []byte(m6.KeyBlob), residual); err != nil {
			t.Fatal(err)
		}
		maps.Copy(r.secrets.seed, stored)
		for key, name := range headSecretRows(t) {
			if _, err := lr.Get(ctx, []byte(key)); err == nil {
				r.secrets.seed[name] = "m2-moved-" + string(name)
			}
		}
	}
}

// corpusGolden is what a corpus run is recorded as: every native row M6 wrote,
// the legacy keys it removed, what it quarantined and what it reported.
type corpusGolden struct {
	Rows        map[string]map[string]any `json:"rows"`
	Removed     []string                  `json:"removed_legacy_keys"`
	Quarantined []goldenQuarantine        `json:"quarantined"`
	Report      []m6.Entry                `json:"report"`
	Verify      goldenVerify              `json:"verify"`
}

type goldenQuarantine struct {
	Key    string `json:"key"`
	Reason string `json:"reason"`
	Step   string `json:"step"`
}

type goldenVerify struct {
	Read, Written, Held, Quarantined int64
	OrphanedRefs                     map[string]int64 `json:",omitempty"`
}

// rowText is a native row for the golden: the JSON a codec-tagged row holds, or
// its text.
func rowText(raw []byte) any {
	if len(raw) > 1 && raw[0] == 'j' {
		var v any
		if json.Unmarshal(raw[1:], &v) == nil {
			return v
		}
	}
	var v any
	if json.Unmarshal(raw, &v) == nil {
		return v
	}
	return string(raw)
}

func recordCorpus(t *testing.T, name string) (*rig, *snap, map[string][]byte, corpusGolden) {
	t.Helper()
	r := newRig(t, fromCorpus(name), afterM2)
	fsys := r.seed(t)
	before := r.before(t, fsys)
	s, err := r.runOn(t, fsys)
	if err != nil {
		t.Fatalf("M6 over the %s corpus: %v\nlogs:\n%s", name, err, r.logs.String())
	}
	if s.State != "done" {
		t.Fatalf("state %q, want done\nlogs:\n%s", s.State, r.logs.String())
	}
	if len(s.Verify.Failures) != 0 {
		t.Fatalf("verify failures: %v", s.Verify.Failures)
	}
	g := corpusGolden{Rows: map[string]map[string]any{}, Report: r.report.Entries()}
	for owner, rows := range s.Rows {
		if len(rows) == 0 {
			continue
		}
		m := map[string]any{}
		for k, v := range rows {
			m[strings.ReplaceAll(k, "\x00\x01", "/")] = rowText(v)
		}
		g.Rows[owner] = m
	}
	for k := range before {
		if _, kept := s.Legacy[k]; !kept {
			g.Removed = append(g.Removed, k)
		}
	}
	slices.Sort(g.Removed)
	for _, q := range s.Quarantine {
		g.Quarantined = append(g.Quarantined, goldenQuarantine{Key: q.Key, Reason: q.Reason, Step: q.Step})
	}
	g.Verify = goldenVerify{Read: s.Verify.Read, Written: s.Verify.Written, Held: s.Verify.Held, Quarantined: s.Verify.Quarantined, OrphanedRefs: s.Verify.OrphanedRefs}
	return r, s, before, g
}

// checkGolden compares g with the recorded file, or rewrites it under
// UPDATE_M6_GOLDEN=1.
func checkGolden(t *testing.T, name string, g corpusGolden) {
	t.Helper()
	got, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join(repoRoot(t), filepath.FromSlash(goldenDir), name+".golden.json")
	if os.Getenv("UPDATE_M6_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the golden (UPDATE_M6_GOLDEN=1 writes it): %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("the %s corpus run differs from %s (UPDATE_M6_GOLDEN=1 rewrites it after review)\n--- got ---\n%s", name, path, got)
	}
}

// writtenByTheBoot are the books rows the cutover boot writes around M6: the
// preference rows (the level step's migration marker, the format version and the
// counter that numbers them) and M6's own rootscan: rows (I4's one exception).
func writtenByTheBoot(k string) bool {
	return strings.HasPrefix(k, "preference:") || k == "counter:preference" || strings.HasPrefix(k, m6.PrefixRootScan)
}

// identicalOutsideM6 is I4: every row M6 did not drain is byte-identical, and no
// row appears that the boot and M6 do not account for.
func identicalOutsideM6(t *testing.T, before, after map[string][]byte) {
	t.Helper()
	for k, v := range before {
		if got, kept := after[k]; kept && !writtenByTheBoot(k) && !bytes.Equal(got, v) {
			t.Errorf("row %q changed", k)
		}
	}
	for k := range after {
		if _, ok := before[k]; !ok && !writtenByTheBoot(k) {
			t.Errorf("row %q appeared in books' range", k)
		}
	}
}

func TestCorpus_Stage1(t *testing.T) {
	_, s, before, g := recordCorpus(t, "stage1")
	if len(g.Quarantined) != 0 {
		t.Errorf("stage1 has no M6 quarantine case, got %v", g.Quarantined)
	}
	identicalOutsideM6(t, before, s.Legacy)
	wantGone := []string{"counter:import_path", "import_path:1", "import_path:path:/tmp/r2corpus/stage1/import", "setting:config_blob"}
	if !slices.Equal(g.Removed, wantGone) {
		t.Errorf("removed %v, want %v", g.Removed, wantGone)
	}
	checkGolden(t, "stage1", g)
}

func TestCorpus_Overlay(t *testing.T) {
	r, s, before, g := recordCorpus(t, "overlay")
	// The overlay's M6 quarantine cases, and only those (F2-13's manifest:
	// corrupt-import-root and corrupt-aijob).
	var got []string
	for _, q := range g.Quarantined {
		got = append(got, q.Key)
	}
	want := []string{"aijob:01M731PC80XRKWT53PV5E2M8AD", "import_path:9"}
	if !slices.Equal(got, want) {
		t.Fatalf("quarantined %v, want exactly %v", got, want)
	}
	// The four importable jobs arrive with their states; the corrupt one does not.
	js := jobs(t, s)
	wantStatus := map[string]string{
		"01M731PC8DNNJSBXAQST2BWP2N": "pending", "01M731PC8R0X608KAZC5B5ADVY": "submitted",
		"01M731PC94TET30BZB3PZNR869": "completed", "01M731PC9GA2RPP9C4QYS3KKW5": "failed",
	}
	if len(js) != len(wantStatus) {
		t.Errorf("%d jobs imported, want %d", len(js), len(wantStatus))
	}
	for id, st := range wantStatus {
		if j, ok := js[id]; !ok || j["state"] != st {
			t.Errorf("job %s: state %v, want %s", id, j["state"], st)
		}
	}
	// oracle-relay: written, kept, reported misconfigured, never quarantined.
	insts := instances(t, s)
	relay, ok := insts["ai/oracle-relay"]
	if !ok {
		t.Fatal("the invalid-URL endpoint oracle-relay was not written")
	}
	if url, _ := settingsOf(relay)["url"].(string); url != "http://oracle.invalid:not-a-port/v1" {
		t.Errorf("oracle-relay url %q, want the raw one kept", url)
	}
	misconfigured := false
	for _, e := range r.report.Entries() {
		if e.ID == "ai/oracle-relay" && strings.Contains(e.Detail, "misconfigured") {
			misconfigured = true
		}
	}
	if !misconfigured {
		t.Errorf("oracle-relay is not reported misconfigured; report: %v", r.report.Entries())
	}
	// Books rows are byte-identical outside the drained families, and the
	// referencing rows of the quarantined root are among them.
	identicalOutsideM6(t, before, s.Legacy)
	for _, k := range []string{"import_path:9"} {
		if _, kept := s.Legacy[k]; kept {
			t.Errorf("%q stays in books' range after being quarantined", k)
		}
	}
	checkGolden(t, "overlay", g)
}
