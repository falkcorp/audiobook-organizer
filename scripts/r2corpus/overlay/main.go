// file: scripts/r2corpus/overlay/main.go
// version: 1.1.0
// guid: 5b0e6c1d-8a2f-4e37-9c41-7d2f0a9b6e18
// last-edited: 2026-11-10

//go:build r2corpus_overlay

// Command overlay writes the hand-built cases of R2's fixture corpus, stage 2
// (F2-13; docs/specs/platform-core/09-abo-on-core.md §9.3 item 1): the rows
// R0's API cannot write, onto a copy of F2-12's stage-1 store.
//
// It is built ONLY at the R0 tag, against that tag's own internal/database
// (scripts/r2corpus/overlay/build_overlay.py checks the tag out in a temporary
// worktree, copies this directory in and builds it there with
// -tags r2corpus_overlay). main no longer has internal/database after R1-26,
// so the build constraint keeps every build and vet of main away from it.
//
// Every row goes through the tag's own code: the store's writers where one
// exists (CreateUser, CreateRole, CreateAPIKey, InsertRunV3/TransitionRunV3,
// usermerge.Service ...), and a JSON encode of the tag's own type plus SetRaw
// where the case is a value no writer produces (a status no handler accepts).
// A corrupt row is a torn JSON value written with SetRaw. Nothing here
// decides a row's format.
//
// Usage:
//
//	overlay -store <copy of stage1> -corpus overlay -report <file.json>
//	overlay -store <copy of stage1> -corpus blocker -case <label> -report <file.json>
//
// The report lists every case written and the keys it wrote, which the driver
// records in manifest.json. A case's keys are not written down by hand: the
// program iterates the whole store before and after each case and records
// every key the case added or changed (and, separately, any it removed), so
// the list holds every row HEAD's writers touched (indexes, counters, audit
// rows) and nothing else. A change to the settings blob is recorded per field
// as setting:config_blob#<field>.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/oklog/ulid/v2"
)

// Fixture constants. Every value is a fixture (invariant 3 of the brief):
// Batman comic characters and titles, .invalid domains, /tmp/r2corpus paths.
const (
	// storeRoot is where every absolute path stage 1 stored points
	// (write_r0_corpus.py Layout); new rows use the same root.
	storeRoot = "/tmp/r2corpus/stage1"
	// overlayPassword is the password of the users this program creates.
	overlayPassword = "fixture-password-arkham-asylum-1974"
	// tornSuffix makes a torn row: a JSON value cut short, as a write lost
	// half-way leaves it. Every corrupt row is its family's JSON prefix plus
	// this, so no decoder accepts it.
	tornSuffix = `,"torn":`
)

type caseReport struct {
	Case string `json:"case"`
	// Keys are the keys the case added or changed, sorted.
	Keys []string `json:"keys"`
	// Removed are the keys the case deleted, sorted (usually none).
	Removed []string `json:"removed,omitempty"`
	// Corrupt are the keys the case wrote a value under that is meant not to
	// decode (setTorn) or not to decrypt (setUndecryptable).
	Corrupt []string `json:"corrupt,omitempty"`
}

type writer struct {
	db      *database.PebbleStore
	reports []caseReport
	// corrupt collects the current case's deliberately broken keys.
	corrupt []string
}

func newID() string { return ulid.Make().String() }

// storeState is every key of the store with a digest of its value, plus the
// settings blob's top-level fields (to name a blob change by field).
type storeState struct {
	rows map[string][32]byte
	blob map[string]json.RawMessage
}

// blobKey is the settings blob's row (internal/config/persistence.go saves the
// whole config there through SetSetting("config_blob", ...)).
const blobKey = "setting:config_blob"

func (w *writer) state() storeState {
	pairs, err := w.db.ScanPrefix("")
	must(err, "iterate the store")
	st := storeState{rows: make(map[string][32]byte, len(pairs))}
	for _, kv := range pairs {
		st.rows[kv.Key] = sha256.Sum256(kv.Value)
		if kv.Key == blobKey {
			var row database.Setting
			must(json.Unmarshal(kv.Value, &row), "decode "+blobKey)
			must(json.Unmarshal([]byte(row.Value), &st.blob), "decode the settings blob")
		}
	}
	return st
}

// diff is what changed from before to after: keys added or changed, keys
// removed. A changed settings blob is reported as its changed fields.
func diff(before, after storeState) (changed, removed []string) {
	for k, v := range after.rows {
		if old, ok := before.rows[k]; ok && old == v {
			continue
		}
		if k == blobKey {
			changed = append(changed, blobFieldChanges(before.blob, after.blob)...)
			continue
		}
		changed = append(changed, k)
	}
	for k := range before.rows {
		if _, ok := after.rows[k]; !ok {
			removed = append(removed, k)
		}
	}
	sort.Strings(changed)
	sort.Strings(removed)
	return changed, removed
}

func blobFieldChanges(before, after map[string]json.RawMessage) []string {
	var out []string
	for f, v := range after {
		if old, ok := before[f]; !ok || !bytes.Equal(old, v) {
			out = append(out, blobKey+"#"+f)
		}
	}
	for f := range before {
		if _, ok := after[f]; !ok {
			out = append(out, blobKey+"#"+f)
		}
	}
	if len(out) == 0 {
		// The row changed but no field did (a re-encode): name the row.
		out = append(out, blobKey)
	}
	return out
}

// quiesce waits until two whole-store iterations 250 ms apart agree, so a
// write the store started on its own at open (a cache reconcile, a warmup) is
// not counted against the first case.
func (w *writer) quiesce() storeState {
	prev := w.state()
	for range 40 {
		time.Sleep(250 * time.Millisecond)
		cur := w.state()
		if sameState(prev, cur) {
			return cur
		}
		prev = cur
	}
	must(fmt.Errorf("the store kept changing on its own for 10 s"), "quiesce")
	return prev
}

func sameState(a, b storeState) bool {
	if len(a.rows) != len(b.rows) {
		return false
	}
	for k, v := range a.rows {
		if b.rows[k] != v {
			return false
		}
	}
	return true
}

// run writes one case and records what it changed.
func (w *writer) run(label string, write func(w *writer, label string)) {
	before := w.state()
	w.corrupt = nil
	write(w, label)
	after := w.state()
	changed, removed := diff(before, after)
	if len(changed) == 0 {
		must(fmt.Errorf("case %s changed no key", label), "write case")
	}
	sort.Strings(w.corrupt)
	w.reports = append(w.reports, caseReport{Case: label, Keys: changed, Removed: removed, Corrupt: w.corrupt})
}

// setJSON encodes v (a type of the tag's own packages) and writes it raw.
func (w *writer) setJSON(key string, v any) {
	data, err := json.Marshal(v)
	must(err, "encode "+key)
	must(w.db.SetRaw(key, data), "write "+key)
}

// setTorn writes a torn value under key: the first part of a JSON object.
func (w *writer) setTorn(key, prefix string) {
	must(w.db.SetRaw(key, []byte(prefix+tornSuffix)), "write torn "+key)
	w.corrupt = append(w.corrupt, key)
}

func must(err error, what string) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "overlay: %s: %v\n", what, err)
		os.Exit(1)
	}
}

func mustUser(db *database.PebbleStore, username string) *database.User {
	u, err := db.GetUserByUsername(username)
	must(err, "read user "+username)
	if u == nil {
		must(fmt.Errorf("stage 1 has no user %q", username), "read user")
	}
	return u
}

func main() {
	store := flag.String("store", "", "the copy of stage1/ to write into (holds db/ and state/)")
	corpus := flag.String("corpus", "", "overlay | blocker")
	label := flag.String("case", "", "the blocker case to write (-corpus blocker)")
	reportPath := flag.String("report", "", "write the case report (JSON) here")
	list := flag.Bool("list", false, "print the case labels this program writes and exit")
	flag.Parse()

	if *list {
		for _, c := range overlayCases {
			fmt.Println("overlay", c.label)
		}
		for _, c := range blockerCases {
			fmt.Println("blocker", c.label)
		}
		return
	}
	if *store == "" || *reportPath == "" {
		must(fmt.Errorf("-store and -report are required"), "usage")
	}
	dbPath := filepath.Join(*store, "db", "audiobooks.pebble")
	if _, err := os.Stat(dbPath); err != nil {
		must(err, "stage-1 copy")
	}
	db, err := database.NewPebbleStore(dbPath)
	must(err, "open "+dbPath)
	w := &writer{db: db}
	w.quiesce()

	switch *corpus {
	case "overlay":
		for _, c := range overlayCases {
			w.run(c.label, c.write)
		}
	case "blocker":
		for _, c := range blockerCases {
			if c.label == *label || c.store == *label {
				w.run(c.label, c.write)
			}
		}
		if len(w.reports) == 0 {
			must(fmt.Errorf("no blocker case or store %q", *label), "usage")
		}
	default:
		must(fmt.Errorf("-corpus must be overlay or blocker, not %q", *corpus), "usage")
	}
	// Nothing may change once the last case is written: a write the store
	// makes on its own would have been counted against some case.
	last := w.state()
	if !sameState(last, w.quiesce()) {
		must(fmt.Errorf("the store changed after the last case"), "verify")
	}
	must(db.Close(), "close store")

	out := w.reports
	data, err := json.MarshalIndent(out, "", "  ")
	must(err, "encode report")
	must(os.WriteFile(*reportPath, append(data, '\n'), 0o644), "write report")
	fmt.Fprintf(os.Stderr, "overlay: wrote %d cases at %s\n", len(out), time.Now().UTC().Format(time.RFC3339))
}

// overlayCase is one labelled case of the overlay; write writes its rows and
// records them under label.
type overlayCase struct {
	label string
	write func(w *writer, label string)
}

// blockerCase is one blocker case. store is the directory under blockers/
// that holds it: its label, except the terminal-merge controls, which share
// one store (the manifest says so).
type blockerCase struct {
	label string
	store string
	write func(w *writer, label string)
}
