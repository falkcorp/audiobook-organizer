// file: internal/books/cutover/m6/report.go
// version: 1.0.1
// guid: 0699342a-cff3-4c41-87a1-25c400aeba93
// last-edited: 2026-11-19

package m6

import (
	"sort"
	"sync"
)

// Entry is one line of the step's report (05-metadata M1.10: "per kind, the
// instances it would write, their IDs, the trust it would carry and every row it
// would quarantine"). Detail never holds a secret or a row's value.
type Entry struct {
	// Kind is instance, trust, job, root, mapping, rule, note or quarantine.
	Kind   string
	ID     string
	Detail string
}

// Report collects the entries of a run, in a Plan and a real run alike.
type Report struct {
	mu      sync.Mutex
	entries []Entry
}

// Entries is a copy of the entries so far, in the order recorded.
func (r *Report) Entries() []Entry {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Entry(nil), r.entries...)
}

// ByKind counts the entries per kind.
func (r *Report) ByKind() map[string]int {
	out := map[string]int{}
	for _, e := range r.Entries() {
		out[e.Kind]++
	}
	return out
}

// IDs is the sorted IDs of the entries of kind.
func (r *Report) IDs(kind string) []string {
	var out []string
	for _, e := range r.Entries() {
		if e.Kind == kind {
			out = append(out, e.ID)
		}
	}
	sort.Strings(out)
	return out
}

// report records e when the run has a Report.
func (m *Migration) report(e Entry) {
	if m.cfg.Report == nil {
		return
	}
	m.cfg.Report.mu.Lock()
	defer m.cfg.Report.mu.Unlock()
	m.cfg.Report.entries = append(m.cfg.Report.entries, e)
}
