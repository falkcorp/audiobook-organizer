// file: internal/books/cutover/m4/stamps.go
// version: 1.0.1
// guid: 3c8e51b7-0d94-4f62-a1e3-9b27d5c40f18
// last-edited: 2026-11-18

package m4

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/kindrename"
	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
	"github.com/falkcorp/mediacore/tasks"
)

const (
	prefixCompletion = "op:completion:"
	prefixDeprev     = "op:deprev:"

	reasonCompletion     = "the completion row's key or value does not decode"
	reasonCompletionKind = "the completion row's operation names no usable kind"
	reasonDeprev         = "the dependency revision row's key or value does not decode"
	// reasonCompletionStray is the reason of every other row of a subject one of
	// whose completion keys does not decode: the subject is set aside whole.
	reasonCompletionStray = "a completion key of the subject does not decode, so the subject is set aside whole"

	OutcomeStamp      = "stamp-imported"
	OutcomeStampStale = "stamp-dropped" // a completion older than its subject's dependency revision
)

// revRow is op:completion: and op:deprev: (store.opDepRevValue).
type revRow struct {
	Rev uint64 `json:"rev"`
}

// completionKey is a decoded op:completion:<type>:<id>:<op type>[:<file id>].
type completionKey struct {
	subjectType, subjectID, opType, fileID string
}

func parseCompletionKey(key string) (completionKey, bool) {
	rest, ok := strings.CutPrefix(key, prefixCompletion)
	if !ok {
		return completionKey{}, false
	}
	parts := strings.Split(rest, ":")
	if len(parts) < 3 || len(parts) > 4 || slices.Contains(parts, "") {
		return completionKey{}, false
	}
	k := completionKey{subjectType: parts[0], subjectID: parts[1], opType: parts[2]}
	if len(parts) == 4 {
		k.fileID = parts[3]
	}
	return k, true
}

// subject is the native subject a completion stamps: the book for a book-level
// completion, the file for a per-file one (5T.8.3: books/books or
// books/book_files).
func (k completionKey) subject() entity.Ref {
	if k.fileID != "" {
		return entity.Ref{Module: "books", Kind: "book_files", ID: k.fileID}
	}
	return subjectRef(k.subjectType + ":" + k.subjectID)
}

// completionSubject is the subject of an op:completion: key that lies under a
// subject's prefix (subjectPrefix), whatever the rest of the key holds: every
// such row is in the subject driver's scan, so it is the driver's to handle
// even when the key does not decode as a completion. ok is false for a key
// that names no subject (no driver ever scans it).
func completionSubject(key string) (subjectType, subjectID string, ok bool) {
	rest, ok := strings.CutPrefix(key, prefixCompletion)
	if !ok {
		return "", "", false
	}
	parts := strings.SplitN(rest, ":", 3)
	if len(parts) < 3 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// subjectPrefix is the key prefix of every completion of one subject.
func subjectPrefix(subjectType, subjectID string) string {
	return prefixCompletion + subjectType + ":" + subjectID + ":"
}

func deprevKey(subjectType, subjectID string) string {
	return prefixDeprev + subjectType + ":" + subjectID
}

// stepSubjectStamps handles one op:completion: row. HEAD holds a subject's
// completions as one row per op type (and per file), each at the revision of the
// subject's dependencies it completed at, and the subject's dependency revision
// beside them; a completion counts only while its revision has not fallen behind
// the subject's (HEAD's own rule, registry/deps.go: stale when completionRev <
// curRev). The native stamp is a time, which HEAD never stored, so every current
// completion is stamped at Config.StampAt. The dependency revision decides
// staleness for every completion of its subject and so must outlive all of them:
// the subject's lowest completion row drives, imports the subject's whole set and
// deletes it with the revision row in one batch; the others leave their rows to
// it, and the revision row defers while any completion of the subject remains.
//
// Every row under the subject's prefix belongs to the subject, including one
// whose key does not decode as a completion (a stray key): the lowest row
// drives whatever its shape, and a stray key anywhere in the subject sets the
// whole subject aside, its revision row with it. A stray row is never set aside
// by its own worker. It once was, while the others deferred to the lowest row:
// when the stray row was the lowest, nothing drove the subject, and whether its
// completions were imported or left behind (failing Verify) hung on whether the
// stray row's quarantine had committed when the others were stepped. Only a
// key that names no subject, which no driver scans, is set aside by itself.
func (m *Migration) stepSubjectStamps(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
	subjectType, subjectID, ok := completionSubject(string(kv.Key))
	if !ok {
		return m.quarantineOne(kv, w, reasonCompletion)
	}
	r := m.legacy()
	rows, err := scanRows(ctx, r, subjectPrefix(subjectType, subjectID))
	if err != nil {
		return err
	}
	if len(rows) == 0 || string(rows[0].Key) != string(kv.Key) {
		// A lower row drives the subject, or its batch took this row since the
		// runner read it.
		m.deferred.Add(1)
		w.Count(OutcomeDeferred)
		return nil
	}
	var curRev uint64
	depKey := []byte(deprevKey(subjectType, subjectID))
	raw, err := r.Get(ctx, depKey)
	// Not found: never bumped, revision 0, as HEAD's GetDepRev reads it.
	found := err == nil
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		return err
	}
	var dep *storage.KV
	if found {
		dep = &storage.KV{Key: depKey, Value: raw}
	}
	if slices.ContainsFunc(rows, func(row storage.KV) bool {
		_, ok := parseCompletionKey(string(row.Key))
		return !ok
	}) {
		return m.quarantineSubject(rows, dep, w)
	}
	if found {
		var d revRow
		if json.Unmarshal(raw, &d) != nil {
			// No completion of the subject can be judged current without it, and
			// deleting the row would let a later pass read revision 0: set the
			// whole subject aside.
			if err := m.quarantineOne(storage.KV{Key: depKey, Value: raw}, w, reasonDeprev); err != nil {
				return err
			}
			for _, row := range rows {
				if err := m.quarantineOne(row, w, reasonDeprev); err != nil {
					return err
				}
			}
			return nil
		}
		curRev = d.Rev
	}

	stamped := map[stampTarget]bool{}
	var order []stampTarget
	for _, row := range rows {
		k, _ := parseCompletionKey(string(row.Key)) // every key decodes: a stray set the subject aside above
		var v revRow
		if json.Unmarshal(row.Value, &v) != nil {
			if err := m.quarantineOne(row, w, reasonCompletion); err != nil {
				return err
			}
			continue
		}
		tg, err := kindrename.Resolve(k.opType)
		if err != nil || tg.Kind == "" {
			if err := m.quarantineOne(row, w, reasonCompletionKind); err != nil {
				return err
			}
			continue
		}
		if tg.Rule == kindrename.RuleRetired || v.Rev < curRev {
			// A retired kind is never required again, and a stale completion is
			// exactly a requirement that is not met.
			w.Count(OutcomeStampStale)
			m.dropped.Add(1)
			if err := w.Delete(row.Key); err != nil {
				return err
			}
			continue
		}
		t := stampTarget{subject: k.subject(), kind: tg.Kind}
		if !stamped[t] {
			stamped[t] = true
			order = append(order, t)
		}
		if err := w.Delete(row.Key); err != nil {
			return err
		}
	}
	for _, t := range order {
		if err := m.imp.Stamp(ctx, w.Batch(), t.subject, t.kind, m.stampAt); err != nil {
			return err
		}
		m.stamps.Add(1)
		w.Count(OutcomeStamp)
	}
	if found {
		return w.Delete(depKey)
	}
	return nil
}

// quarantineSubject sets a subject with a stray completion key aside whole: each
// stray row with the decode reason, every other completion and the revision row
// (dep, when the subject has one) with the stray reason.
func (m *Migration) quarantineSubject(rows []storage.KV, dep *storage.KV, w *datamigrate.StepWriter) error {
	for _, row := range rows {
		why := reasonCompletionStray
		if _, ok := parseCompletionKey(string(row.Key)); !ok {
			why = reasonCompletion
		}
		if err := m.quarantineOne(row, w, why); err != nil {
			return err
		}
	}
	if dep != nil {
		return m.quarantineOne(*dep, w, reasonCompletionStray)
	}
	return nil
}

// stampTarget is one native stamp: two HEAD completions (an old op ID and its
// alias, say) can name the same one.
type stampTarget struct {
	subject entity.Ref
	kind    tasks.Kind
}

// stepDeprev handles one op:deprev: row: it goes with the last completion of its
// subject, whose driver deletes it; a revision with no completion left is
// deleted here.
func (m *Migration) stepDeprev(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
	rest, ok := strings.CutPrefix(string(kv.Key), prefixDeprev)
	subjectType, subjectID, found := strings.Cut(rest, ":")
	if !ok || !found || subjectType == "" || subjectID == "" {
		return m.quarantineOne(kv, w, reasonDeprev)
	}
	r := m.legacy()
	left, err := scanRows(ctx, r, subjectPrefix(subjectType, subjectID))
	if err != nil {
		return err
	}
	// Read the row after the scan: the driver's commit is atomic, so the row
	// still being here proves the scan did not come after it.
	if _, err := r.Get(ctx, kv.Key); errors.Is(err, storage.ErrNotFound) {
		m.deferred.Add(1)
		w.Count(OutcomeDeferred)
		return nil // the subject's driver took it
	} else if err != nil {
		return err
	}
	if len(left) > 0 {
		// Whatever the row holds, the subject's driver reads it to judge the
		// completions and deletes or sets it aside with them.
		m.deferred.Add(1)
		w.Count(OutcomeDeferred)
		return nil
	}
	var d revRow
	if json.Unmarshal(kv.Value, &d) != nil {
		return m.quarantineOne(kv, w, reasonDeprev)
	}
	m.dropped.Add(1)
	w.Count(OutcomeStampStale)
	return w.Delete(kv.Key)
}
