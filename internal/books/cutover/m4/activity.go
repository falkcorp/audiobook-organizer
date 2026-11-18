// file: internal/books/cutover/m4/activity.go
// version: 1.0.0
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

	reasonActivityKey  = "the activity row's key does not decode"
	reasonActivityBody = "the activity row does not decode"
)

// activityKey is act:<tier>:<20 digit unix nano>:<ulid>.
var activityKey = regexp.MustCompile(`^act:([a-z]+):([0-9]{20}):([^:]+)$`)

// ActivityConfig is what the rewrite needs besides its declaration.
type ActivityConfig struct {
	// Logger receives one line per held row (its key's class, never a
	// value). nil discards.
	Logger *slog.Logger
}

// Activity is the rewrite's state: its own accounting. Step calls are
// independent (the runner steps shards concurrently).
type Activity struct {
	logger                *slog.Logger
	rewritten, kept, held atomic.Int64
}

// NewActivity returns the in-place activity rewrite with ID id.
//
// raisesLevel and after are the flip's, as for New; the migration is run after
// M4 (After M4's ID is the caller's to add to after).
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
	a := &Activity{logger: cfg.Logger}
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
		Verify: func(context.Context, storage.Reader) (datamigrate.VerifyReport, error) {
			return datamigrate.VerifyReport{
				Read:    a.rewritten.Load() + a.kept.Load() + a.held.Load(),
				Written: a.rewritten.Load(),
				Held:    a.held.Load(),
			}, nil
		},
	}, nil
}

// kindOf is the kind a stored HEAD def ID is renamed to: the generated entry's,
// by Lookup, not Resolve, so a type that is no def ID (a feed type like
// scan_complete) is never turned into a retired kind. ok is false for such a
// type, and for repairs.plan and repairs.apply, whose kind needs a run's
// fixer_id an activity row does not carry: those stay as stored.
func kindOf(id string) (string, bool) {
	t, ok := kindrename.Lookup(id)
	if !ok || t.Kind == "" {
		return "", false
	}
	return string(t.Kind), true
}

// Step rewrites one primary row.
func (a *Activity) Step(_ context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
	m := activityKey.FindStringSubmatch(string(kv.Key))
	if m == nil {
		return a.hold(kv, w, reasonActivityKey)
	}
	tier := m[1]
	var row map[string]json.RawMessage
	if json.Unmarshal(kv.Value, &row) != nil || row == nil {
		return a.hold(kv, w, reasonActivityBody)
	}
	oldType, newType, changed, ok := rewriteEntry(tier, row)
	if !ok {
		return a.hold(kv, w, reasonActivityBody)
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
// ok is false for a row whose renamed fields do not decode.
func rewriteEntry(tier string, row map[string]json.RawMessage) (oldType, newType string, changed, ok bool) {
	if raw, has := row["type"]; has {
		if json.Unmarshal(raw, &oldType) != nil {
			return "", "", false, false
		}
	}
	newType = oldType
	if tier != "digest" {
		if k, found := kindOf(oldType); found {
			newType = k
			row["type"], _ = json.Marshal(k)
			changed = true
		}
	}
	if tags, c, good := renameTags(row["tags"]); !good {
		return "", "", false, false
	} else if c {
		row["tags"] = tags
		changed = true
	}
	if raw, has := row["details"]; has && string(raw) != "null" {
		var det map[string]json.RawMessage
		if json.Unmarshal(raw, &det) != nil {
			return "", "", false, false
		}
		dc := false
		if tier == "digest" {
			c, good := renameDigestItems(det)
			if !good {
				return "", "", false, false
			}
			dc = c
		} else if id, has := det["def_id"]; has {
			var s string
			if json.Unmarshal(id, &s) == nil {
				if k, found := kindOf(s); found {
					det["def_id"], _ = json.Marshal(k)
					dc = true
				}
			}
		}
		if dc {
			out, err := encode(det)
			if err != nil {
				return "", "", false, false
			}
			row["details"] = out
			changed = true
		}
	}
	return oldType, newType, changed, true
}

// renameTags rewrites the def:<id> tags of a tags array (nil or null: none).
func renameTags(raw json.RawMessage) (json.RawMessage, bool, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return raw, false, true
	}
	var tags []string
	if json.Unmarshal(raw, &tags) != nil {
		return nil, false, false
	}
	changed := false
	for i, t := range tags {
		if id, ok := strings.CutPrefix(t, "def:"); ok {
			if k, found := kindOf(id); found {
				tags[i] = "def:" + k
				changed = true
			}
		}
	}
	if !changed {
		return raw, false, true
	}
	out, err := encode(tags)
	return out, err == nil, err == nil
}

// renameDigestItems renames the type and def: tags of each item of a daily
// digest's details.
func renameDigestItems(det map[string]json.RawMessage) (changed, ok bool) {
	raw, has := det["items"]
	if !has || string(raw) == "null" {
		return false, true
	}
	var items []map[string]json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return false, false
	}
	for _, it := range items {
		if tr, has := it["type"]; has {
			var s string
			if json.Unmarshal(tr, &s) != nil {
				return false, false
			}
			if k, found := kindOf(s); found {
				it["type"], _ = json.Marshal(k)
				changed = true
			}
		}
		tags, c, good := renameTags(it["tags"])
		if !good {
			return false, false
		}
		if c {
			it["tags"] = tags
			changed = true
		}
	}
	if !changed {
		return false, true
	}
	out, err := encode(items)
	if err != nil {
		return false, false
	}
	det["items"] = out
	return true, true
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
