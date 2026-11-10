// file: scripts/r2corpus/overlay/main.go
// version: 1.0.0
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
// records in manifest.json.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
	Case string   `json:"case"`
	Keys []string `json:"keys"`
}

type writer struct {
	db      *database.PebbleStore
	reports map[string]*caseReport
	order   []string
}

func newID() string { return ulid.Make().String() }

// record notes that case label wrote keys.
func (w *writer) record(label string, keys ...string) {
	r, ok := w.reports[label]
	if !ok {
		r = &caseReport{Case: label}
		w.reports[label] = r
		w.order = append(w.order, label)
	}
	r.Keys = append(r.Keys, keys...)
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
}

// checkKeysPresent fails when a key a case recorded is not in the store, so
// the manifest never names a row that is not there. A settings-blob field key
// (setting:config_blob#<field>) is checked by its row.
func (w *writer) checkKeysPresent() {
	for _, l := range w.order {
		for _, k := range w.reports[l].Keys {
			row := k
			if i := strings.IndexByte(k, '#'); i >= 0 {
				row = k[:i]
			}
			v, err := w.db.GetRaw(row)
			must(err, "read back "+row)
			if v == nil {
				must(fmt.Errorf("case %s recorded %s, which is not in the store", l, k), "read back")
			}
		}
	}
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
	w := &writer{db: db, reports: map[string]*caseReport{}}

	switch *corpus {
	case "overlay":
		for _, c := range overlayCases {
			c.write(w, c.label)
		}
	case "blocker":
		found := false
		for _, c := range blockerCases {
			if c.label == *label || c.store == *label {
				c.write(w, c.label)
				found = true
			}
		}
		if !found {
			must(fmt.Errorf("no blocker case or store %q", *label), "usage")
		}
	default:
		must(fmt.Errorf("-corpus must be overlay or blocker, not %q", *corpus), "usage")
	}
	w.checkKeysPresent()
	must(db.Close(), "close store")

	out := make([]caseReport, 0, len(w.order))
	for _, l := range w.order {
		r := w.reports[l]
		sort.Strings(r.Keys)
		out = append(out, *r)
	}
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
