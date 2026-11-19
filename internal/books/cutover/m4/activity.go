// file: internal/books/cutover/m4/activity.go
// version: 1.4.0
// guid: 5b1f8d36-c4a7-4e09-9d52-0a63e7b1c8f4
// last-edited: 2026-11-18

package m4

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/kindrename"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
	"github.com/falkcorp/mediacore/tasks"
)

// The activity half of 5T.8.2's kind rename: the feed's rows name HEAD's def IDs
// in an entry's type, in details.def_id and in a def:<id> tag, and the renamed
// kinds replace them in place. The activity families are read-write in books'
// claims (the feed keeps being written after the flip), so this cannot be a
// Cutover step, whose Source is retired claims only (05-storage §5S.3.13; the
// Residual mechanism covers exact keys only). It is its own in-place migration,
// registered after M4 by the caller.

// activityTiers are the tiers whose primary rows are rewritten: every primary
// tier of the feed (pebble_activity_store.go), the daily digest included.
// act:op:, act:src:, act:bk: and act:lvl: name no def ID and are untouched;
// act:typ: is rewritten with the primary row that owns each entry of it.
var activityTiers = []string{"audit", "batch", "change", "debug", "digest", "info", "system"}

// ActivitySourceRanges is the rewrite's Source: one range per primary tier.
func ActivitySourceRanges() []storage.Range {
	var out []storage.Range
	for _, t := range activityTiers {
		p := "act:" + t + ":"
		out = append(out, storage.Range{Start: []byte(p), End: prefixEnd(p)})
	}
	return out
}

// Outcomes of the activity rewrite.
const (
	OutcomeActivityRewritten = "activity-rewritten"
	OutcomeActivityKept      = "activity-unchanged"
	// OutcomeActivityItemsHeld counts, once per item, the digest items left as
	// stored inside a row that was otherwise written (or kept): the item is a
	// repairs reference no run can resolve to one fixer. The row's other items
	// are still renamed (holding the row would leave them under their HEAD def
	// IDs too), so each such item is counted here and logged, and the row is
	// also recorded once through Hold, with its items' reasons, so it can be
	// found after the boot. The runner's own "held" outcome therefore counts
	// such a row as well as the rewritten or unchanged outcome it also gets.
	OutcomeActivityItemsHeld = "activity-items-held"

	reasonActivityKey  = "the activity row's key does not decode"
	reasonActivityBody = "the activity row does not decode"
	// reasonActivityRepairs holds a repairs row (type, details.def_id or def:
	// tag repairs.plan or repairs.apply) whose kind cannot be found: its kind
	// is books.repairs.<fixer_id>, and only the run it belongs to knows the
	// fixer. The row has no operation_id, or the run is not in the task store
	// (never imported, quarantined or pruned), or the run's kind is not a
	// repairs kind. It stays as stored, listed, rather than naming a def ID
	// the task store no longer has.
	reasonActivityRepairs = "a repairs row whose run is not in the task store as a repairs run, so its fixer is unknown"
	// reasonActivityItem and reasonActivityItemAmbiguous are the same decision
	// taken for one digest item, which stays as stored while the digest's other
	// items are renamed.
	reasonActivityItem          = "a digest item of a repairs def whose operation is not in the task store as a repairs run, so its fixer is unknown"
	reasonActivityItemAmbiguous = "a digest item of a repairs def whose operations are runs of different fixers, so it has no single kind"
	// reasonActivityItemSampled holds an item whose op: tags may be a sample.
	reasonActivityItemSampled = "a digest item of a repairs def from a Summarize row whose operation list may be a sample, so its fixer cannot be known"
)

// digestOpSampleMax is how many op: tags a Summarize row keeps, and so the most
// a digest item built from one can carry. It is a pinned copy of
// summarizeOpSampleMax in internal/books/store/activity_types.go (introduced at
// 20 by ed93cd5f0, 2026-09-19, never changed): m4 sits at layer 0 and cannot
// import the store, and the constant is unexported there.
// TestDigestOpSampleMax_MatchesSummarize fails if the two diverge.
//
// Summarize groups change-tier rows by (day, type, source), not by operation,
// and keeps the lowest summarizeOpSampleMax operation IDs as op: tags. Its
// distinct_ops and op_ids_truncated live in the summary's Details, which a
// DigestItem does not carry. An item with this many op: tags may therefore be
// missing the operations of other fixers, so resolving its fixer from the tags
// it has could rename it wrongly, and the rename cannot be undone. The only
// marker a DigestItem has is the op: tags themselves (DigestItemSource); its
// Summary text ("Summary: N <type> entries ...") could narrow the exactly-20
// case but is display text, not a contract, so it is not parsed.
const digestOpSampleMax = 20

// RunReader is the task store's read of one run record: tasks.RunStore.Get.
// It returns an error wrapping tasks.ErrRunNotFound for a run it does not hold.
type RunReader interface {
	Get(ctx context.Context, id tasks.RunID) (tasks.RunRecord, error)
}

// activityKey is act:<tier>:<20 digit unix nano>:<ulid>.
var activityKey = regexp.MustCompile(`^act:([a-z]+):([0-9]{20}):([^:]+)$`)

// ActivityConfig is what the rewrite needs besides its declaration.
type ActivityConfig struct {
	// Runs is the task store's run reader, bound by the runner after the store
	// is open (like Config.Legacy), over the runs M4 imported: a repairs row
	// (repairs.plan, repairs.apply) is renamed to its run's kind,
	// books.repairs.<fixer_id>, and only the run knows the fixer. Required.
	Runs func() RunReader
	// Logger receives one line per held row (its key's class, never a
	// value). nil discards.
	Logger *slog.Logger
}

// Activity is the rewrite's state: its own accounting. Step calls are
// independent (the runner steps shards concurrently).
type Activity struct {
	logger                *slog.Logger
	runs                  func() RunReader
	rewritten, kept, held atomic.Int64
	// itemRows counts the rows that were written or kept with at least one
	// digest item left as stored. Each is also recorded through Hold, so Verify
	// reports it as held, but it is one of rewritten or kept for Read.
	itemRows atomic.Int64
}

// NewActivity returns the in-place activity rewrite with ID id.
//
// raisesLevel and after are the flip's, as for New, with M4's own ref among
// them when it is meant to run after M4 (Steps adds it).
//
// Parallelism is CPU(): rows are independent. A worker writes its own primary
// row and the one act:typ: index key that names it (the key carries the row's
// own tier, nanosecond and ULID, so no other row owns it), and nothing else, so
// two workers never write one key; the write to the row's own key is inside
// Source, hence RewritesInPlace.
func NewActivity(cfg ActivityConfig, id string, raisesLevel int, after []storage.StepRef) (datamigrate.DataMigration, error) {
	switch {
	case id == "":
		return datamigrate.DataMigration{}, errors.New("m4: the activity step ID is required")
	case raisesLevel <= 0:
		return datamigrate.DataMigration{}, errors.New("m4: raisesLevel is the flip's books level, above 0")
	case len(after) == 0:
		return datamigrate.DataMigration{}, errors.New("m4: after holds books' level step and M4")
	}
	if cfg.Runs == nil {
		return datamigrate.DataMigration{}, errors.New("m4: ActivityConfig.Runs is the task store's run reader (a repairs row's kind comes from its run)")
	}
	a := &Activity{logger: cfg.Logger, runs: cfg.Runs}
	if a.logger == nil {
		a.logger = slog.New(slog.DiscardHandler)
	}
	return datamigrate.DataMigration{
		ID:              id,
		Owner:           "books",
		Description:     "HEAD def IDs in the activity feed's rows renamed to the task kinds (in place)",
		Source:          ActivitySourceRanges(),
		Parallelism:     tasks.CPU(),
		Mode:            datamigrate.BootBlocking,
		Compat:          storage.Breaking,
		RaisesLevel:     raisesLevel,
		After:           append([]storage.StepRef(nil), after...),
		RewritesInPlace: true,
		Step:            a.Step,
		// Read counts every row once, however many items of it were left as
		// stored; Held also counts the rows written or kept with such items,
		// which are recorded through Hold.
		Verify: func(context.Context, storage.Reader) (datamigrate.VerifyReport, error) {
			return datamigrate.VerifyReport{
				Read:    a.rewritten.Load() + a.kept.Load() + a.held.Load(),
				Written: a.rewritten.Load(),
				Held:    a.held.Load() + a.itemRows.Load(),
			}, nil
		},
	}, nil
}

// kindOf is the kind a stored HEAD def ID is renamed to: the generated entry's,
// by Lookup, not Resolve, so a type that is no def ID (a feed type like
// scan_complete) is never turned into a retired kind. ok is false for such a
// type. repairs.plan and repairs.apply have a Marker and no Kind (the fixer
// names it per run): they report repairs true, and the caller asks the run.
func kindOf(id string) (kind string, repairs, ok bool) {
	t, found := kindrename.Lookup(id)
	if !found {
		return "", false, false
	}
	if t.Kind == "" {
		return "", t.Marker == kindrename.MarkerRepairsPlan || t.Marker == kindrename.MarkerRepairsApply, false
	}
	return string(t.Kind), false, true
}

// errNoRepairsKind says a repairs reference could not be resolved to a kind.
var errNoRepairsKind = errors.New("m4: a repairs reference has no run to take its kind from")

// errAmbiguousRepairsKind says a repairs reference names several runs that do
// not share one kind. It is an errNoRepairsKind: the reference stays as stored.
var errAmbiguousRepairsKind = fmt.Errorf("%w: its runs are of different kinds", errNoRepairsKind)

// errSampledRepairsKind says a repairs reference's operation list may be a
// Summarize sample (digestOpSampleMax). It is an errNoRepairsKind too.
var errSampledRepairsKind = fmt.Errorf("%w: its operation list may be a sample", errNoRepairsKind)

// opRefs is the operations a reference belongs to. sampled is set when ids came
// from a digest item's op: tags and there are digestOpSampleMax or more of them,
// so the list may be cut.
type opRefs struct {
	ids     []string
	sampled bool
}

// kinds resolves the def IDs of one feed row. opID is the operation the
// reference belongs to (the row's operation_id; a digest item's op: tag).
type kinds struct {
	ctx  context.Context
	runs RunReader
	// cache holds the kind each run resolved to, so a digest of many items of
	// one run reads it once.
	cache map[string]string
	// heldItems holds the reason of each digest item the row's rewrite left as
	// stored because its repairs reference could not be resolved. The caller
	// counts and logs them.
	heldItems []string
}

// kindForOps is kind for a reference that names ops: none (as kind with no
// operation), one, or several. With several, every run must resolve to the same
// kind, else errAmbiguousRepairsKind (errNoRepairsKind if any one cannot be
// resolved). A def that is no repairs def ignores ops, as kind does.
func (k *kinds) kindForOps(def string, refs opRefs) (string, bool, error) {
	if _, repairs, _ := kindOf(def); repairs && refs.sampled {
		return "", false, errSampledRepairsKind
	}
	ops := refs.ids
	if len(ops) <= 1 {
		op := ""
		if len(ops) == 1 {
			op = ops[0]
		}
		return k.kind(def, op)
	}
	var first string
	for i, op := range ops {
		kind, found, err := k.kind(def, op)
		switch {
		case err != nil || !found:
			return "", false, err
		case i == 0:
			first = kind
		case kind != first:
			return "", false, errAmbiguousRepairsKind
		}
	}
	return first, true, nil
}

// kind is the kind def is renamed to, found false when def is no def ID. A
// repairs def is resolved from the run opID names; errNoRepairsKind when there
// is none, the run is absent or its kind is not books.repairs.<fixer_id>, and
// any other error from the reader as it is (the step fails and is retried).
func (k *kinds) kind(def, opID string) (string, bool, error) {
	kind, repairs, ok := kindOf(def)
	switch {
	case ok:
		return kind, true, nil
	case !repairs:
		return "", false, nil
	case opID == "":
		return "", false, errNoRepairsKind
	}
	if got, hit := k.cache[opID]; hit {
		if got == "" {
			return "", false, errNoRepairsKind
		}
		return got, true, nil
	}
	rec, err := k.runs.Get(k.ctx, tasks.RunID(opID))
	switch {
	case errors.Is(err, tasks.ErrRunNotFound):
		k.cache[opID] = ""
		return "", false, errNoRepairsKind
	case err != nil:
		return "", false, fmt.Errorf("m4: read run %q for an activity row: %w", opID, err)
	case !strings.HasPrefix(string(rec.Kind), kindrename.RepairsKindPrefix):
		k.cache[opID] = ""
		return "", false, errNoRepairsKind
	}
	k.cache[opID] = string(rec.Kind)
	return string(rec.Kind), true, nil
}

// Step rewrites one primary row.
func (a *Activity) Step(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
	m := activityKey.FindStringSubmatch(string(kv.Key))
	if m == nil {
		return a.hold(kv, w, reasonActivityKey)
	}
	tier := m[1]
	var row map[string]json.RawMessage
	if json.Unmarshal(kv.Value, &row) != nil || row == nil {
		return a.hold(kv, w, reasonActivityBody)
	}
	ks := &kinds{ctx: ctx, runs: a.runs(), cache: map[string]string{}}
	oldType, newType, changed, ok, err := rewriteEntry(tier, row, ks)
	switch {
	case errors.Is(err, errNoRepairsKind):
		return a.hold(kv, w, reasonActivityRepairs)
	case err != nil:
		return err
	case !ok:
		return a.hold(kv, w, reasonActivityBody)
	}
	for _, reason := range ks.heldItems {
		a.logger.Warn("m4: a digest item is left as stored", "reason", reason, "key_prefix", keyPrefixOf(kv.Key))
		w.Count(OutcomeActivityItemsHeld)
	}
	if len(ks.heldItems) > 0 {
		// The row is written (or kept) too: Hold only leaves a durable record of
		// the full key, which the log line above does not carry.
		a.itemRows.Add(1)
		var distinct []string // each reason once, in the order first met
		for _, r := range ks.heldItems {
			if !slices.Contains(distinct, r) {
				distinct = append(distinct, r)
			}
		}
		w.Hold(kv.Key, fmt.Sprintf("%d digest item(s) left as stored: %s", len(ks.heldItems), strings.Join(distinct, "; ")))
	}
	if !changed {
		a.kept.Add(1)
		w.Count(OutcomeActivityKept)
		return nil
	}
	out, err := encode(row)
	if err != nil {
		return err
	}
	if err := w.Set(kv.Key, out); err != nil {
		return err
	}
	if newType != oldType {
		// The type index entry names the type in its key: delete the old key and
		// put the new one, in this row's batch, so the index never disagrees with
		// the row it points at. An empty type has no index entry.
		ref := []byte(string(kv.Key)[len("act:"):])
		if oldType != "" {
			if err := w.Delete(typeIndexKey(oldType, tier, m[2], m[3])); err != nil {
				return err
			}
		}
		if err := w.Set(typeIndexKey(newType, tier, m[2], m[3]), ref); err != nil {
			return err
		}
	}
	a.rewritten.Add(1)
	w.Count(OutcomeActivityRewritten)
	return nil
}

// hold sets a row aside in place: the runner records it and its reason and
// lists it, and the row stays in the feed. A non-Cutover step cannot quarantine
// (the copy-and-delete is the cutover steps'), and a live feed row that no
// reader can decode costs nothing to leave where it is.
func (a *Activity) hold(kv storage.KV, w *datamigrate.StepWriter, reason string) error {
	a.logger.Warn("m4: an activity row is held", "reason", reason, "key_prefix", keyPrefixOf(kv.Key))
	a.held.Add(1)
	w.Hold(kv.Key, reason)
	return nil
}

// keyPrefixOf is the key's first two components, safe to log.
func keyPrefixOf(key []byte) string {
	parts := strings.SplitN(string(key), ":", 3)
	if len(parts) < 2 {
		return ""
	}
	return parts[0] + ":" + parts[1]
}

// typeIndexKey is act:typ:<type>:<tier>:<nanos>:<ulid>
// (pebble_activity_filter_index.go). The store escapes ':' and '%' in the type
// component; the types this step puts in a key are HEAD def IDs and the kinds
// they map to, whose grammars contain neither, so the escape is the identity
// here and is not reproduced.
func typeIndexKey(typ, tier, nanos, ulid string) []byte {
	return []byte("act:typ:" + typ + ":" + tier + ":" + nanos + ":" + ulid)
}

// rewriteEntry renames the def IDs in one decoded row in place. changed is
// whether any field changed; oldType and newType are the entry's own type
// before and after (the digest tier's own type, daily_digest, never changes).
// ok is false for a row whose renamed fields do not decode. err is
// errNoRepairsKind for a repairs reference with no kind to take, or a reader
// error; the row is then not to be written (it may be half renamed).
func rewriteEntry(tier string, row map[string]json.RawMessage, ks *kinds) (oldType, newType string, changed, ok bool, err error) {
	var opID string
	if raw, has := row["operation_id"]; has {
		if json.Unmarshal(raw, &opID) != nil {
			opID = "" // a non-string operation_id names no run
		}
	}
	if raw, has := row["type"]; has {
		if json.Unmarshal(raw, &oldType) != nil {
			return "", "", false, false, nil
		}
	}
	newType = oldType
	if tier != "digest" {
		k, found, err := ks.kind(oldType, opID)
		if err != nil {
			return "", "", false, false, err
		}
		if found {
			enc, err := json.Marshal(k)
			if err != nil {
				return "", "", false, false, fmt.Errorf("m4: encode kind %q: %w", k, err)
			}
			newType = k
			row["type"] = enc
			changed = true
		}
	}
	tags, c, good, err := renameTags(row["tags"], opsOf(opID), ks)
	if err != nil {
		return "", "", false, false, err
	}
	if !good {
		return "", "", false, false, nil
	}
	if c {
		row["tags"] = tags
		changed = true
	}
	if raw, has := row["details"]; has && string(raw) != "null" {
		var det map[string]json.RawMessage
		if json.Unmarshal(raw, &det) != nil {
			return "", "", false, false, nil
		}
		dc := false
		if tier == "digest" {
			c, good, err := renameDigestItems(det, ks)
			if err != nil {
				return "", "", false, false, err
			}
			if !good {
				return "", "", false, false, nil
			}
			dc = c
		} else if id, has := det["def_id"]; has {
			var s string
			if json.Unmarshal(id, &s) == nil {
				k, found, err := ks.kind(s, opID)
				if err != nil {
					return "", "", false, false, err
				}
				if found {
					enc, err := json.Marshal(k)
					if err != nil {
						return "", "", false, false, fmt.Errorf("m4: encode kind %q: %w", k, err)
					}
					det["def_id"] = enc
					dc = true
				}
			}
		}
		if dc {
			out, err := encode(det)
			if err != nil {
				return "", "", false, false, nil
			}
			row["details"] = out
			changed = true
		}
	}
	return oldType, newType, changed, true, nil
}

// opsOf is the operations a row's own operation_id names: none or one.
func opsOf(opID string) opRefs {
	if opID == "" {
		return opRefs{}
	}
	return opRefs{ids: []string{opID}}
}

// renameTags rewrites the def:<id> tags of a tags array (nil or null: none).
// ops are the operations the tags belong to, for a repairs def.
func renameTags(raw json.RawMessage, ops opRefs, ks *kinds) (out json.RawMessage, changed, ok bool, err error) {
	if len(raw) == 0 || string(raw) == "null" {
		return raw, false, true, nil
	}
	var tags []string
	if json.Unmarshal(raw, &tags) != nil {
		return nil, false, false, nil
	}
	for i, t := range tags {
		if id, has := strings.CutPrefix(t, "def:"); has {
			k, found, err := ks.kindForOps(id, ops)
			if err != nil {
				return nil, false, false, err
			}
			if found {
				tags[i] = "def:" + k
				changed = true
			}
		}
	}
	if !changed {
		return raw, false, true, nil
	}
	enc, err := encode(tags)
	return enc, err == nil, err == nil, nil
}

// opsOfTags is the operations a digest item's tags name: each op:<id> tag, in
// order (a repeated one resolves from the run cache and agrees with itself). A Summarize item that spans several operations has several.
func opsOfTags(raw json.RawMessage) []string {
	var tags []string
	if json.Unmarshal(raw, &tags) != nil {
		return nil
	}
	var ids []string
	for _, t := range tags {
		if v, has := strings.CutPrefix(t, "op:"); has && v != "" {
			ids = append(ids, v)
		}
	}
	return ids
}

// itemOps is the operations a digest item belongs to: its op: tags (sampled
// when there are digestOpSampleMax or more), else its own operation_id (digest
// items written before 2026-05-20 carry no tags), else none.
func itemOps(it map[string]json.RawMessage) opRefs {
	if ids := opsOfTags(it["tags"]); len(ids) > 0 {
		return opRefs{ids: ids, sampled: len(ids) >= digestOpSampleMax}
	}
	var opID string
	if raw, has := it["operation_id"]; has && json.Unmarshal(raw, &opID) != nil {
		opID = "" // a non-string operation_id names no run
	}
	return opsOf(opID)
}

// renameDigestItems renames the type and def: tags of each item of a daily
// digest's details. An item's operations are itemOps. An item whose repairs
// reference cannot be resolved (errNoRepairsKind) keeps its values as stored,
// whole, and its reason is added to ks.heldItems; the other items are still
// renamed. When any item is renamed the items array is re-encoded, so every
// item's values are preserved but its keys are written in sorted order. ok is
// false when an item does not decode.
func renameDigestItems(det map[string]json.RawMessage, ks *kinds) (changed, ok bool, err error) {
	raw, has := det["items"]
	if !has || string(raw) == "null" {
		return false, true, nil
	}
	var items []map[string]json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return false, false, nil
	}
	for _, it := range items {
		c, good, err := renameDigestItem(it, ks)
		switch {
		case errors.Is(err, errSampledRepairsKind):
			ks.heldItems = append(ks.heldItems, reasonActivityItemSampled)
			continue
		case errors.Is(err, errAmbiguousRepairsKind):
			ks.heldItems = append(ks.heldItems, reasonActivityItemAmbiguous)
			continue
		case errors.Is(err, errNoRepairsKind):
			ks.heldItems = append(ks.heldItems, reasonActivityItem)
			continue
		case err != nil:
			return false, false, err
		case !good:
			return false, false, nil
		}
		changed = changed || c
	}
	if !changed {
		return false, true, nil
	}
	out, err := encode(items)
	if err != nil {
		return false, false, nil
	}
	det["items"] = out
	return true, true, nil
}

// renameDigestItem renames one item's type and def: tags, or none of them: the
// item is changed only when everything it names resolved.
func renameDigestItem(it map[string]json.RawMessage, ks *kinds) (changed, ok bool, err error) {
	ops := itemOps(it)
	var newType json.RawMessage
	if tr, has := it["type"]; has {
		var s string
		if json.Unmarshal(tr, &s) != nil {
			return false, false, nil
		}
		k, found, err := ks.kindForOps(s, ops)
		if err != nil {
			return false, false, err
		}
		if found {
			if newType, err = json.Marshal(k); err != nil {
				return false, false, fmt.Errorf("m4: encode kind %q: %w", k, err)
			}
		}
	}
	tags, c, good, err := renameTags(it["tags"], ops, ks)
	if err != nil {
		return false, false, err
	}
	if !good {
		return false, false, nil
	}
	if newType != nil {
		it["type"] = newType
		changed = true
	}
	if c {
		it["tags"] = tags
		changed = true
	}
	return changed, true, nil
}

// encode is json.Marshal without HTML escaping, so a rewritten row's other
// fields keep the bytes' meaning and, where they held no escapes, their form.
func encode(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("m4: encode: %w", err)
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
