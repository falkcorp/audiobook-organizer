// file: core/integrations/notify/store.go
// version: 1.0.0
// guid: b4e92a17-0c5d-4d38-8f61-7d3a9e50c2b8
// last-edited: 2026-11-13

package notify

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/codec"
	"github.com/falkcorp/mediacore/storage/keys"
)

// Owner is the storage owner of the c/notify/ keyspace, where rules live at
// c/notify/rules/<id> (05-notify §3). The kernel registers it with
// StorageSpec when notify flips on (F7-09).
const Owner storage.Owner = "core/notify"

// rulesSeg is the first key segment of every rule row.
const rulesSeg = "rules"

// StorageSpec is the c/notify/ spec the kernel registers for Owner: a core
// keyspace whose rule rows are Serialized, so every write to one carries an
// Expect (05-storage R6-1) and two writers of one rule conflict instead of
// losing a write.
func StorageSpec() storage.Spec {
	return storage.Spec{Keyspace: true, Serialized: []string{string(rulesPrefix())}}
}

func rulesPrefix() []byte { return keys.Prefix(keys.Str(rulesSeg)) }

func ruleKey(id string) []byte { return keys.Join(keys.Str(rulesSeg), keys.Str(id)) }

// RuleStore persists Rules (R4-1). SDK surface. Core ships NativeRuleStore over
// c/notify/rules/<id> as its only implementation (no app supplies its own).
// PutRule and DeleteRule commit their own batch under storage.RetryOnConflict;
// the mutex that serialises the Service's swap of its in-memory rule set is
// taken before the commit and never inside it.
type RuleStore interface {
	// Load returns every stored rule, by ID.
	Load(ctx context.Context) ([]Rule, error)
	// Put stages r into b, replacing any rule of that ID.
	Put(ctx context.Context, b *storage.Batch, r Rule) error
	// Delete stages the removal of rule id into b; ErrRuleNotFound if none is
	// stored.
	Delete(ctx context.Context, b *storage.Batch, id string) error
}

// ruleRow is the stored form of a Rule, codec.JSON[ruleRow](1): severities and
// instance refs as their printed names, so a row reads as the admin wrote it.
type ruleRow struct {
	ID          string   `json:"id"`
	Topics      []string `json:"topics"`
	MinSeverity string   `json:"min_severity"`
	Instances   []string `json:"instances"`
	Enabled     bool     `json:"enabled"`
}

// ruleCodec is the row codec: a function, because a package-level value is
// mutable state (03 R5).
func ruleCodec() codec.Codec[ruleRow] { return codec.JSON[ruleRow](1) }

func encodeRule(r Rule) ([]byte, error) {
	row := ruleRow{ID: r.ID, Topics: r.Topics, MinSeverity: r.MinSeverity.String(), Enabled: r.Enabled}
	for _, ref := range r.Instances {
		row.Instances = append(row.Instances, ref.String())
	}
	return ruleCodec().Encode(row)
}

func decodeRule(raw []byte) (Rule, error) {
	row, err := ruleCodec().Decode(raw)
	if err != nil {
		return Rule{}, err
	}
	sev, ok := ParseSeverity(row.MinSeverity)
	if !ok {
		return Rule{}, fmt.Errorf("min_severity %q is not a severity", row.MinSeverity)
	}
	r := Rule{ID: row.ID, Topics: row.Topics, MinSeverity: sev, Enabled: row.Enabled}
	for _, s := range row.Instances {
		kind, id, ok := strings.Cut(s, "/")
		if !ok {
			return Rule{}, fmt.Errorf("instance %q is not <kind>/<id>", s)
		}
		r.Instances = append(r.Instances, integrations.Ref{Kind: integrations.Kind(kind), Instance: integrations.InstanceID(id)})
	}
	return r, nil
}

// NativeRuleStore is the RuleStore over ks, the c/notify/ keyspace.
func NativeRuleStore(ks storage.Keyspace) RuleStore { return nativeStore{ks: ks} }

type nativeStore struct{ ks storage.Keyspace }

// Load reads every row. A key under rules/ that is not one segment, or a row
// that does not decode, fails the load: a store whose rules cannot be told
// apart must not be served as a shorter list, because a rule that silently
// routes nothing is a notification lost.
func (s nativeStore) Load(ctx context.Context) ([]Rule, error) {
	var out []Rule
	for kv, err := range s.ks.ScanPrefix(ctx, rulesPrefix()) {
		if err != nil {
			return nil, fmt.Errorf("notify: read c/notify/rules/: %w", err)
		}
		segs, err := keys.Split(kv.Key)
		if err != nil || len(segs) != 2 || string(segs[0]) != rulesSeg || len(segs[1]) == 0 {
			return nil, fmt.Errorf("notify: c/notify/ key %q is not a rule row", kv.Key)
		}
		r, err := decodeRule(kv.Value)
		if err != nil {
			return nil, fmt.Errorf("notify: rule row %q: %w", segs[1], err)
		}
		if r.ID != string(segs[1]) {
			return nil, fmt.Errorf("notify: rule row %q holds rule %q", segs[1], r.ID)
		}
		out = append(out, r)
	}
	return out, nil
}

// read binds the keyspace to b and reads the row with an Expect on its
// committed value; nil is an absent row.
func (s nativeStore) read(ctx context.Context, b *storage.Batch, id string) ([]byte, error) {
	if b == nil {
		return nil, errors.New("notify: a rule write needs the caller's batch")
	}
	if !validRuleID(id) {
		return nil, invalidf("ID %q must be 1 to 32 characters of [a-z][a-z0-9-]*", id)
	}
	b.Bind(s.ks)
	raw, err := b.GetForUpdate(ctx, s.ks, ruleKey(id))
	if errors.Is(err, storage.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("notify: read rule %s: %w", id, err)
	}
	return raw, nil
}

func (s nativeStore) Put(ctx context.Context, b *storage.Batch, r Rule) error {
	if _, err := s.read(ctx, b, r.ID); err != nil {
		return err
	}
	val, err := encodeRule(r)
	if err != nil {
		return fmt.Errorf("notify: encode rule %s: %w", r.ID, err)
	}
	if err := b.In(s.ks).Set(ruleKey(r.ID), val); err != nil {
		return fmt.Errorf("notify: stage rule %s: %w", r.ID, err)
	}
	return nil
}

func (s nativeStore) Delete(ctx context.Context, b *storage.Batch, id string) error {
	raw, err := s.read(ctx, b, id)
	if err != nil {
		return err
	}
	if raw == nil {
		return fmt.Errorf("%w: %s", ErrRuleNotFound, id)
	}
	if err := b.In(s.ks).Delete(ruleKey(id)); err != nil {
		return fmt.Errorf("notify: stage the delete of rule %s: %w", id, err)
	}
	return nil
}

// Importer is what an app's cutover step calls to write its pre-core routing
// as native rules (R2 step M6; 05-notify §10). The kernel builds it over
// c/notify/rules/ and hands it to the step in module.Importers (06 §6.4.2).
// ImportRule validates r as PutRule does, except the author's topic
// permissions (the step runs as the app's system actor), and stages the row
// into b, so the rule commits with the step's deletion of the rows it came
// from. It is create-only (05-storage §5S.3.13): an absent rule is written; a
// rule already holding exactly r is current and stages nothing, so a re-run of
// a step that committed is a no-op; any other row is storage.ErrImportConflict.
type Importer interface {
	ImportRule(ctx context.Context, b *storage.Batch, r Rule) error
}

// ImporterOption configures NativeImporter.
type ImporterOption func(*nativeImporter)

// WithCatalog gives the importer the events catalog it validates a rule's
// topics against. NativeImporter(ks) alone has none, and then every ImportRule
// fails with ErrNoCatalog: an importer that skipped the check would let a
// migrated rule name a topic nothing publishes.
func WithCatalog(cat events.Catalog) ImporterOption {
	return func(i *nativeImporter) { i.cat = cat }
}

// NativeImporter is the Importer over ks, the c/notify/ keyspace. The spec's
// NativeImporter(ks) has no way to give it the catalog it validates topics
// against, so the catalog is an option (F7-08 and F7-09 pass WithCatalog).
func NativeImporter(ks storage.Keyspace, opts ...ImporterOption) Importer {
	i := &nativeImporter{store: nativeStore{ks: ks}}
	for _, o := range opts {
		o(i)
	}
	return i
}

type nativeImporter struct {
	store nativeStore
	cat   events.Catalog
}

func (i *nativeImporter) ImportRule(ctx context.Context, b *storage.Batch, r Rule) error {
	if _, err := validateRule(r, i.cat); err != nil {
		return err
	}
	raw, err := i.store.read(ctx, b, r.ID)
	if err != nil {
		return err
	}
	val, err := encodeRule(r)
	if err != nil {
		return fmt.Errorf("notify: encode rule %s: %w", r.ID, err)
	}
	if raw != nil {
		if bytes.Equal(raw, val) {
			return nil
		}
		return fmt.Errorf("%w: notify rule %s already has a row with other content", storage.ErrImportConflict, r.ID)
	}
	if err := b.In(i.store.ks).Set(ruleKey(r.ID), val); err != nil {
		return fmt.Errorf("notify: stage the import of rule %s: %w", r.ID, err)
	}
	return nil
}
