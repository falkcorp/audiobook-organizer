// file: internal/books/cutover/m6/quarantine_test.go
// version: 1.0.1
// guid: 6c1f8e30-9b27-4d54-a083-e5d2b7c1f946
// last-edited: 2026-11-19

package m6_test

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m6"
	"github.com/falkcorp/mediacore/storage"
)

// The quarantine reasons are fixed classes an operator reads in the report, so
// the tests pin the words.
const (
	rJobUndecodable   = "the aijob row does not decode"
	rJobState         = "the aijob row's status is not one HEAD wrote"
	rJobKey           = "the aijob row's id is not its key"
	rJobConflict      = "core already holds a different job under this ID"
	rOrphanPayload    = "the aijob_payload row has no aijob row"
	rOrphanBatch      = "the aijob_batch index row names no aijob row that holds it"
	rBlobUndecodable  = "the settings blob does not decode"
	rRootUndecodable  = "the import_path row does not decode"
	rRootConflict     = "core already holds a different root"
	rUnprovenURL      = "a credential cannot be ruled out of the URL"
	rInstanceConflict = "core already holds a different instance under this ID"
	rAliasNoRoot      = "the path alias names no root M6 registers"
	rRootPath         = "the root path is not an absolute host path"
	rUnreadMember     = "a settings blob member that no part of M6 reads"
)

const memberPrefix = m6.KeyBlob + "#"

// withBlob edits the fixture's blob tree.
func withBlob(edit func(tree map[string]any)) func(*rig) {
	return func(r *rig) {
		tree := blobTree()
		edit(tree)
		withRows(map[string][]byte{m6.KeyBlob: blobRow(r.t, tree)})(r)
	}
}

func conflict(kind, id string) func(*rig) {
	return func(r *rig) {
		r.refuse = func(k, i string) error {
			if k == kind && i == id {
				return fmt.Errorf("a different one is stored: %w", storage.ErrImportConflict)
			}
			return nil
		}
	}
}

// TestQuarantine_Cases: every row M6 cannot convert is moved aside with its raw
// bytes under a fixed reason, never overwritten and never dropped (I1), the rest
// of its group still migrates, and the step still completes.
func TestQuarantine_Cases(t *testing.T) {
	_, base := runFixture(t)
	baseline := map[string]string{}
	for _, q := range base.Quarantine {
		baseline[q.Key] = q.Reason
	}

	cases := []struct {
		name string
		opts []func(*rig)
		want map[string]string // key -> reason, besides the baseline
		raw  map[string]string // key -> the bytes quarantine must hold
	}{
		{
			name: "job row is torn",
			opts: []func(*rig){withRows(map[string][]byte{"aijob:01JTORN": []byte(`{"id":"01JTORN","type":`)})},
			want: map[string]string{"aijob:01JTORN": rJobUndecodable},
			raw:  map[string]string{"aijob:01JTORN": `{"id":"01JTORN","type":`},
		},
		{
			name: "job id is not its key",
			opts: []func(*rig){withRows(map[string][]byte{"aijob:01JJOKER": []byte(`{"id":"01JRIDDLER","type":"dedup_review","status":"pending"}`)})},
			want: map[string]string{"aijob:01JJOKER": rJobKey},
		},
		{
			name: "job status HEAD never wrote",
			opts: []func(*rig){withRows(map[string][]byte{"aijob:01JSCARECROW": []byte(`{"id":"01JSCARECROW","type":"dedup_review","status":"fear"}`)})},
			want: map[string]string{"aijob:01JSCARECROW": rJobState},
		},
		{
			name: "payload and batch rows of no job",
			opts: []func(*rig){withRows(map[string][]byte{
				"aijob_payload:01JNOBODY": []byte(`{"x":1}`),
				"aijob_batch:batch-ghost": []byte("01JGHOST"),
			})},
			want: map[string]string{"aijob_payload:01JNOBODY": rOrphanPayload, "aijob_batch:batch-ghost": rOrphanBatch},
		},
		{
			name: "batch index row names another job",
			opts: []func(*rig){withRows(map[string][]byte{
				"aijob:01JPENGUIN":           jobRow(t, "01JPENGUIN", "dedup_review", "batch-shared", "submitted", 0),
				"aijob_batch:batch-shared":   []byte("01JOSWALD"),
				"aijob_payload:01JPENGUIN":   []byte(`{"p":1}`),
				"aijob:01JOSWALD":            jobRow(t, "01JOSWALD", "dedup_review", "batch-shared", "submitted", 0),
				"aijob_payload:01JOSWALD":    []byte(`{"p":2}`),
				"aijob_batch:batch-unshared": []byte("01JOSWALD"),
			})},
			// 01JPENGUIN is imported (its index row names a different job, so the
			// index row stays for 01JOSWALD to take); only the unrelated index row
			// that names no job holding it is quarantined.
			want: map[string]string{"aijob_batch:batch-unshared": rOrphanBatch},
		},
		{
			name: "core already holds a different job",
			opts: []func(*rig){conflict("job", "01JKNIGHTFALL")},
			// The job's payload and index rows stay behind when the job is
			// refused and are quarantined as the orphans they now are, never lost.
			want: map[string]string{"aijob:01JKNIGHTFALL": rJobConflict, "aijob_payload:01JKNIGHTFALL": rOrphanPayload, "aijob_batch:batch-knightfall": rOrphanBatch},
		},
		{
			name: "import_path row is torn",
			opts: []func(*rig){withRows(map[string][]byte{"import_path:7": []byte(`{"id":7,"path":`)})},
			want: map[string]string{"import_path:7": rRootUndecodable},
		},
		{
			name: "import_path id is not its key",
			opts: []func(*rig){withRows(map[string][]byte{"import_path:5": []byte(`{"id":6,"path":"/batcave/import/ivy","name":"Ivy","enabled":true}`)})},
			want: map[string]string{"import_path:5": rRootUndecodable},
		},
		{
			name: "import_path key is not canonical",
			opts: []func(*rig){withRows(map[string][]byte{"import_path:007": []byte(`{"id":7,"path":"/batcave/import/ivy","name":"Ivy","enabled":true}`)})},
			want: map[string]string{"import_path:007": rRootUndecodable},
		},
		{
			name: "import_path is not an absolute path",
			opts: []func(*rig){withRows(map[string][]byte{"import_path:6": []byte(`{"id":6,"path":"gotham/relative","name":"Rel","enabled":true}`)})},
			want: map[string]string{"import_path:6": rRootPath},
		},
		{
			name: "two import_path rows at one path keep the lower ID",
			opts: []func(*rig){withRows(map[string][]byte{"import_path:8": []byte(`{"id":8,"path":"` + importOne + `/","name":"Again","enabled":true}`)})},
			want: map[string]string{"import_path:8": rRootConflict},
		},
		{
			name: "import_path at the library root's path",
			opts: []func(*rig){withRows(map[string][]byte{"import_path:9": []byte(`{"id":9,"path":"` + rootDir + `","name":"Same","enabled":true}`)})},
			want: map[string]string{"import_path:9": rRootConflict},
		},
		{
			name: "path alias of no root",
			opts: []func(*rig){withBlob(func(tree map[string]any) {
				tree["path_aliases"] = append(tree["path_aliases"].([]any), map[string]any{"root": "/nowhere", "windows": "N:"})
			})},
			want: map[string]string{memberPrefix + "path_aliases.1": rAliasNoRoot},
		},
		{
			name: "blob member no step reads",
			opts: []func(*rig){withBlob(func(tree map[string]any) {
				tree["batcomputer_uplink_mode"] = "burst"
				tree["empty_member"] = ""
			})},
			want: map[string]string{memberPrefix + "batcomputer_uplink_mode": rUnreadMember},
			raw:  map[string]string{memberPrefix + "batcomputer_uplink_mode": `"burst"`},
		},
		{
			name: "webhook URL whose userinfo cannot be ruled out",
			opts: []func(*rig){withBlob(func(tree map[string]any) {
				tree["plugins"].(map[string]any)["webhook"].(map[string]any)["settings"].(map[string]any)["urls"] = "http://192.0.2.30:9000/hook, ,http://bat%zz@192.0.2.9/hook"
			})},
			want: map[string]string{memberPrefix + "plugins.webhook.urls.2": rUnprovenURL},
			raw:  map[string]string{memberPrefix + "plugins.webhook.urls.2": "http://bat%zz@192.0.2.9/hook"},
		},
		{
			name: "core already holds a different instance",
			opts: []func(*rig){conflict("instance", "ai/local-uv")},
			want: map[string]string{memberPrefix + "ai_endpoints.1": rInstanceConflict},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, s := runFixture(t, c.opts...)
			got := map[string]string{}
			vals := map[string]string{}
			for _, q := range s.Quarantine {
				if _, dup := got[q.Key]; dup {
					t.Errorf("%s was quarantined twice", q.Key)
				}
				got[q.Key] = q.Reason
				vals[q.Key] = q.Value
			}
			want := maps.Clone(baseline)
			maps.Copy(want, c.want)
			if !maps.Equal(got, want) {
				t.Errorf("quarantine\n got %v\nwant %v\nlogs:\n%s", got, want, r.logs.String())
			}
			for k, raw := range c.raw {
				if vals[k] != raw {
					t.Errorf("%s holds %q, want the raw %q", k, vals[k], raw)
				}
			}
			if len(s.Verify.Failures) != 0 {
				t.Errorf("verify failures: %v", s.Verify.Failures)
			}
			// A quarantined source row is gone from books' range (it lives in
			// quarantine now); nothing else of a drained family is left.
			for k := range c.want {
				if !strings.HasPrefix(k, memberPrefix) {
					if _, still := s.Legacy[k]; still {
						t.Errorf("%s is both quarantined and still in books' range", k)
					}
				}
			}
		})
	}
}

// TestQuarantine_ConflictKeepsTheFirstInstance: two blob members that name one
// instance ID (the second after the rename) never write over each other; the
// second is quarantined whole.
func TestQuarantine_ConflictKeepsTheFirstInstance(t *testing.T) {
	_, s := runFixture(t, withBlob(func(tree map[string]any) {
		eps := tree["ai_endpoints"].([]any)
		dup := map[string]any{"id": "Local_LLM", "label": "Second", "protocol": "openai_compat", "url": "http://192.0.2.11:8080/v1", "chat_model": "other"}
		tree["ai_endpoints"] = append(eps, dup)
	}))
	got := instances(t, s)
	if st := settingsOf(got["ai/local-llm"]); st["text_model"] != "alfred-7b" {
		t.Errorf("the first local-llm was overwritten: %v", st)
	}
	found := false
	for _, q := range s.Quarantine {
		if q.Key == memberPrefix+"ai_endpoints.2" && q.Reason == rInstanceConflict {
			found = true
		}
	}
	if !found {
		t.Errorf("the second member was not quarantined: %v", s.Quarantine)
	}
}

// TestQuarantine_UndecodableBlobStillMigratesRoots: a blob that does not decode is
// kept whole in quarantine and the import_path rows, which do not depend on it,
// still become roots.
func TestQuarantine_UndecodableBlobStillMigratesRoots(t *testing.T) {
	_, s := runFixture(t, withRows(map[string][]byte{m6.KeyBlob: []byte(`{"key":"config_blob","value":"{not json`)}))
	var blob *quarantined
	for i := range s.Quarantine {
		if s.Quarantine[i].Key == m6.KeyBlob {
			blob = &s.Quarantine[i]
		}
	}
	if blob == nil || blob.Reason != rBlobUndecodable {
		t.Fatalf("the blob was not quarantined whole: %v", s.Quarantine)
	}
	if len(instances(t, s)) != 0 {
		t.Error("instances were invented from an unreadable blob")
	}
	if !strings.Contains(string(s.Rows[string(ownerConfig)]["core.library.roots"]), "import-1") {
		t.Error("the import roots did not migrate")
	}
}

// TestIntegrationsMigration_UserinfoMovedToSecret: a credential in a URL is
// split out and kept as a secret under the instance's name; the stored URL and
// every integrations row carry none.
func TestIntegrationsMigration_UserinfoMovedToSecret(t *testing.T) {
	_, s := runFixture(t, withBlob(func(tree map[string]any) {
		tree["plugins"].(map[string]any)["webhook"].(map[string]any)["settings"].(map[string]any)["urls"] = "http://robin:grayson@192.0.2.40/hook"
	}))
	row := instances(t, s)["notify/webhook-1"]
	st := settingsOf(row)
	if u, _ := st["url"].(string); strings.Contains(u, "robin") || strings.Contains(u, "grayson") {
		t.Errorf("the stored URL carries its credential: %q", u)
	}
	names := slices.Sorted(maps.Keys(s.Rows[string(ownerSecrets)]))
	found := false
	for _, n := range names {
		if n == "integrations.notify.webhook-1.userinfo" {
			found = true
			if v := string(s.Rows[string(ownerSecrets)][n]); v != "robin:grayson" {
				t.Errorf("userinfo secret holds %q", v)
			}
		}
	}
	if !found {
		t.Errorf("no userinfo secret staged; secrets %v", names)
	}
	// No native webhook setting reads a userinfo secret, so the instance does not
	// reference it: the credential is kept under the instance's name for the
	// admin to wire in, not dropped and not written into a row.
	if _, ok := st["userinfo"]; ok {
		t.Errorf("the instance carries an unreadable userinfo setting: %v", st)
	}
	for k, v := range s.Rows[string(ownerInteg)] {
		if strings.Contains(string(v), "grayson") {
			t.Errorf("row %q holds the credential", k)
		}
	}
}
