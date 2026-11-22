// file: internal/books/cutover/m6/step.go
// version: 1.0.4
// guid: ff3e2f5f-1fc0-4835-b579-944568abeb07
// last-edited: 2026-11-19

// Package m6 is books' cutover step M6, integrations (03 section 3.8 row M6;
// F7-08): the instance, AI-job, webhook and root data HEAD kept in the settings
// blob and in the aijob*, import_path: and provider_* families, moved into core's
// native stores through its importers, with the HEAD rows deleted in the same
// batch.
//
// The step is dark until F7-09 registers it: nothing here is reached from
// internal/books/cutover's step list or from main. It takes the importers as
// parameters and imports neither core/internal/* (an app module cannot) nor
// internal/books/cutover (which imports this package once the step list exists),
// so the step ID comes in as a parameter too.
//
// Parallelism and the group pass. The step is tasks.Sequential, one shard in key
// order, and RewritesInPlace, for one reason: its work is not row-local. Three
// rows (aijob:, aijob_payload:, aijob_batch:) are one job; the blob, every
// import_path: row and the counter are one config group whose roots list,
// instance set and webhook rule are each ONE importer call over all of its
// inputs. The first row of a group the shard reaches is therefore the one visit
// that reads the whole group (pulling the rest forward from books' legacy range)
// and deletes it, which core's runner admits for a RewritesInPlace step by
// recording the pulled keys and skipping them when the shard reaches them. Two
// workers on one group would double-import it, so the work is partitioned into
// groups, and a group is never split across shards: with a single shard that
// holds by construction. The families are bounded by the install (tens of import
// roots, hundreds of jobs, one blob), and the per-row work is a decode and an
// importer call that stages into the chunk batch, not a library-scale scan.
package m6

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/ai"
	"github.com/falkcorp/mediacore/integrations/notify"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
	"github.com/falkcorp/mediacore/tasks"
)

// Families M6 drains (05-storage 5S.9.1 rows 7, 16 to 19).
const (
	PrefixAIJob        = "aijob:"
	PrefixAIJobBatch   = "aijob_batch:"
	PrefixAIJobPayload = "aijob_payload:"
	KeyImportCounter   = "counter:import_path"
	PrefixDirty        = "idx:sidx:dirty:"
	PrefixImportPath   = "import_path:"
	PrefixDailyBudget  = "provider_daily_budget:"
	PrefixThrottle     = "provider_throttle:"
	KeyBlob            = "setting:config_blob"
)

// Owner is the owner every migrated root names (05-library 5L.10).
const Owner = "books"

// Outcomes the step counts beside the runner's "quarantined". The first group
// are source rows (each consumed row has exactly one), the second are what the
// importers were given.
const (
	OutcomeJob          = "job-imported"
	OutcomeJobFailed    = "job-imported-failed"
	OutcomeRoot         = "root-imported"
	OutcomeIndexRow     = "path-index-dropped"
	OutcomeCounter      = "counter-dropped"
	OutcomeBlob         = "blob-deleted"
	OutcomeDirty        = "dirty-dropped"
	OutcomeHold         = "provider-hold-dropped"
	OutcomeInstance     = "instance-imported"
	OutcomeLocalGrant   = "local-granted"
	OutcomeSecretCopy   = "secret-copied"
	OutcomeUserinfo     = "userinfo-moved"
	OutcomeRule         = "rule-imported"
	OutcomeMapping      = "mapping-imported"
	OutcomeProtected    = "protected-path-imported"
	OutcomeNestedRoot   = "nested-root-registered"
	OutcomeSecretAbsent = "secret-absent"
)

// Quarantine reasons: fixed classes, never a decoder's text (a JSON error quotes
// the value it rejected).
const (
	reasonJobUndecodable   = "the aijob row does not decode"
	reasonJobState         = "the aijob row's status is not one HEAD wrote"
	reasonJobKey           = "the aijob row's id is not its key"
	reasonJobConflict      = "core already holds a different job under this ID"
	reasonJobConflictOwned = "the aijob row this row belongs to was not imported: core already holds a different job under its ID"
	reasonOrphanPayload    = "the aijob_payload row has no aijob row"
	reasonOrphanBatch      = "the aijob_batch index row names no aijob row that holds it"
	reasonBlobUndecodable  = "the settings blob does not decode"
	reasonRootUndecodable  = "the import_path row does not decode"
	reasonRootConflict     = "core already holds a different root"
	reasonRefused          = "core's importer refused the value"
	reasonUnmappable       = "the iTunes mapping's To is under no library root"
	reasonUnknownRow       = "a row of a family M6 drains that no decoder reads"
	reasonUnprovenURL      = "a credential cannot be ruled out of the URL"
	reasonListRefused      = "core refused the config list this row feeds"
	reasonInstanceConflict = "core already holds a different instance under this ID"
	reasonAliasNoRoot      = "the path alias's root is no library root and no ancestor of one: its display rendering is not carried"
	// reasonAliasSubRoot: core's aliases are per root (core/library Alias has no
	// root of its own), so an alias of a path below a root, as HEAD seeds from an
	// iTunes mapping whose To is below one, has no representation.
	reasonAliasSubRoot = "the path alias's root is inside a library root, not at one, and core's aliases are per root: its display rendering is not carried"
	// reasonAliasShadowed: HEAD renders a path through the first alias in list
	// order whose root contains it (web/src/utils/pathAliases.ts matchAlias).
	reasonAliasShadowed = "every library root at or under the path alias's root matched an alias listed before it first: HEAD rendered none of their paths with it, and its display rendering is not carried"
	// reasonAliasShadowedByEarlier: the alias's root is inside a library root, but
	// an alias listed before it has a root that contains that root, so HEAD's
	// first-match rendering took every path under it first and never used it.
	reasonAliasShadowedByEarlier = "the path alias's root is inside a library root, but an alias listed before it has a root that contains its root: HEAD rendered none of its paths with it, and its display rendering is not carried"
	reasonDroppedValue           = "a value HEAD selected by nothing with, left out of the migrated instance: kept here so it can be recovered"
	reasonUnreadCredential       = "a secret setting of a metadata source that HEAD never read for it, or that another of its secrets overrode, and its adapter declares no field for: left unbound, its secret stays in the store"
	reasonMappingRootRefused     = "core refused the library roots this iTunes mapping's To resolves against"
	reasonRootPath               = "the root path is not an absolute host path"
	reasonUnreadMember           = "a settings blob member that no part of M6 reads"
	reasonWebhookEvent           = "the webhook rule's events map to no topic"
)

// Config is what the step needs besides its declaration.
type Config struct {
	// The four importers of module.Importers, and the secrets importer M2 uses.
	Integrations integrations.Importer
	AI           ai.Importer
	Notify       notify.Importer
	Config       config.Importer
	Secrets      secrets.Importer

	// SecretNames lists every name the secrets store holds, unreadable ones
	// included (M2 marks one it could not decrypt with ImportUnreadable, which
	// leaves the name), and SecretValue reads one's value (found false when
	// absent or unreadable). M2 moved the blob's credentials into the store
	// under the names of 05-metadata M1.10's table and left no trace of them in
	// the blob, so the store is where M6 learns which refs an instance has.
	SecretNames func(ctx context.Context) ([]secrets.Name, error)
	SecretValue func(ctx context.Context, n secrets.Name) (value string, found bool, err error)

	// Legacy is books' legacy range: where Step reads the rows it pulls forward
	// and the blob (a lazy accessor: the range exists only after the store is open).
	Legacy func() storage.Reader

	// Effective is the destination view in effect at cutover for the instance ref:
	// the URL settings (settings field to URL) an operator supplies over the stored
	// row, which at HEAD is the process environment or the server's config file. It
	// is the overlay core's operator layer applies on top of the stored layer, and
	// an instance's trust (Local, the secrets origin and the operator-secret
	// origins) is signed from the stored destinations with this overlay applied
	// (seams.d/F7-01 rows 17 and 22: the effective view wins over 05-metadata
	// M1.10's "stored origins"). A value is the URL as supplied (empty ones are no
	// overlay); a nil or empty map means none. For download/deluge the overlay is
	// also the instance's own URL: HEAD's Deluge URL is environment-or-file
	// authoritative over the stored blob, including a stored empty value
	// (internal/config applyEnvAuthoritativeConfig), so the blob's may be empty or
	// stale. Required: a caller with no overlay passes a function returning nil.
	Effective func(ctx context.Context, ref integrations.Ref) (map[string]string, error)
	// EnableAIParsing is HEAD's enable_ai_parsing in effect at cutover. M2 moves
	// it to modules.books.ai.enable_ai_parsing and leaves nothing of it in the
	// blob, so M6 cannot learn it from its own input; it decides whether the
	// ai/openai row M6 synthesises for HEAD's legacy OpenAI path is enabled
	// (legacyKeyCapabilities). Read only when that row is synthesised. The caller
	// answers the effective value, the operator's layer over the stored one, with
	// HEAD's own default when neither is set. Required.
	EnableAIParsing func(ctx context.Context) (bool, error)
	// Report receives one entry per instance, trust grant, root and quarantined
	// row. nil keeps none.
	Report *Report
	// Logger receives one line per quarantined row (its key, a class, never its
	// value). nil discards.
	Logger *slog.Logger
}

// Migration is the step's state: the importers, the report and the accounting.
// Step keeps no per-chunk state: a group is read and deleted in one chunk, so a
// replay after a crash starts from the rows still present.
type Migration struct {
	cfg    Config
	by     string
	logger *slog.Logger

	mu sync.Mutex
	// seen is every source row this boot has read, whether the shard visited it or
	// a group pulled it forward, and handled the ones it consumed or quarantined.
	// Verify holds them to each other: a row that was read and then neither
	// consumed nor quarantined is a row the step lost track of.
	seen    map[string]bool
	handled map[string]bool
	out     map[string]int64
	quar    int64
}

// SourceRanges is the step's Source: the retired claims M6 drains, in key order.
// The blob is an exact key (a range of the key and the next byte).
func SourceRanges() []storage.Range {
	prefix := func(p string) storage.Range {
		end := []byte(p)
		end[len(end)-1]++
		return storage.Range{Start: []byte(p), End: end}
	}
	exact := func(k string) storage.Range {
		return storage.Range{Start: []byte(k), End: append([]byte(k), 0)}
	}
	return []storage.Range{
		prefix(PrefixAIJob), prefix(PrefixAIJobBatch), prefix(PrefixAIJobPayload),
		exact(KeyImportCounter), prefix(PrefixDirty), prefix(PrefixImportPath),
		prefix(PrefixDailyBudget), prefix(PrefixThrottle), exact(KeyBlob),
	}
}

// New returns the data migration for the step with ID id.
//
// raisesLevel and after are the flip's, as every cutover step takes them:
// F7-09 passes its books level and the StepRef of its level step. They come from
// the caller because the level constants live in package cutover, which imports
// this one.
func New(cfg Config, id string, raisesLevel int, after []storage.StepRef) (datamigrate.DataMigration, error) {
	switch {
	case cfg.Integrations == nil || cfg.AI == nil || cfg.Notify == nil || cfg.Config == nil || cfg.Secrets == nil:
		return datamigrate.DataMigration{}, errors.New("m6: all five importers are required")
	case id == "":
		return datamigrate.DataMigration{}, errors.New("m6: the step ID is required")
	case raisesLevel <= 0:
		return datamigrate.DataMigration{}, errors.New("m6: raisesLevel is the flip's books level, above 0")
	case len(after) == 0:
		return datamigrate.DataMigration{}, errors.New("m6: after is books' level step")
	}
	if err := cfg.checkReads(); err != nil {
		return datamigrate.DataMigration{}, err
	}
	m := newMigration(cfg, id)
	return datamigrate.DataMigration{
		ID:          id,
		Owner:       "books",
		Description: "integration instances, AI jobs, webhooks and import roots into core's stores",
		Source:      SourceRanges(),
		Parallelism: tasks.Sequential("job rows and the config group (blob, import roots, counter) are each one unit of work that must be read whole and written once: see the package comment"),
		Mode:        datamigrate.BootBlocking,
		Compat:      storage.Breaking,
		RaisesLevel: raisesLevel,
		After:       append([]storage.StepRef(nil), after...),
		Cutover:     true,
		// The residual blob can hold a credential when M2 could not move it, and
		// a quarantined blob member (setting:config_blob#<member>, under the same
		// prefix) is the raw JSON of a source that may carry userinfo. An
		// import_path row holds a path, a name and scan counters, never a
		// credential, so it is not sealed: a quarantined root stays readable to a
		// listing without the keyring.
		Sensitive:       []string{KeyBlob},
		RewritesInPlace: true,
		Step:            m.Step,
		Verify:          m.Verify,
	}, nil
}

// checkReads refuses a Config missing something the step reads (as opposed to
// writes), which is all Plan needs as well.
func (c Config) checkReads() error {
	switch {
	case c.SecretNames == nil || c.SecretValue == nil:
		return errors.New("m6: Config.SecretNames and Config.SecretValue are required")
	case c.Effective == nil:
		return errors.New("m6: Config.Effective is required (a caller with no operator overlay passes a function returning nil)")
	case c.EnableAIParsing == nil:
		return errors.New("m6: Config.EnableAIParsing is required (HEAD's enable_ai_parsing in effect, which M2 moved out of the blob)")
	case c.Legacy == nil:
		return errors.New("m6: Config.Legacy is books' legacy range")
	}
	return nil
}

func newMigration(cfg Config, id string) *Migration {
	m := &Migration{cfg: cfg, by: id, logger: cfg.Logger, seen: map[string]bool{}, handled: map[string]bool{}, out: map[string]int64{}}
	if m.logger == nil {
		m.logger = slog.New(slog.DiscardHandler)
	}
	return m
}

// writes is where one Step call's effects go: the runner's StepWriter in a boot,
// a recorder that keeps nothing in Plan. Step's code is the same either way; the
// importers it calls and the writes it makes are what differ.
type writes interface {
	Batch() *storage.Batch
	Delete(key []byte) error
	Set(key, value []byte) error
	Count(outcome string)
	Quarantine(key, raw []byte, reason string) error
}

// chunk is one Step call's writes.
type chunk struct {
	m *Migration
	w writes
}

// batch is the chunk's batch, which the importers stage into.
func (c *chunk) batch() *storage.Batch { return c.w.Batch() }

// consume deletes a source row the step has handled and counts it under
// outcome. Every source row is consumed or quarantined exactly once, whether Step
// was called for it or it was pulled forward, which is what Verify checks.
func (c *chunk) consume(key string, outcome string) error {
	if err := c.w.Delete([]byte(key)); err != nil {
		return err
	}
	c.w.Count(outcome)
	c.m.tally(key, outcome)
	return nil
}

// quarantine moves a source row (or a synthetic member key of it) aside with its
// raw bytes, deleting the source key, and counts it.
func (c *chunk) quarantine(key string, raw []byte, reason string) error {
	c.m.logger.Warn("m6: a row is quarantined", "key", key, "reason", reason)
	c.m.report(Entry{Kind: "quarantine", ID: key, Detail: reason})
	if err := c.w.Quarantine([]byte(key), raw, reason); err != nil {
		return err
	}
	c.m.tallyQuarantine(key)
	return nil
}

// note counts a derived outcome (an instance, a grant) that is not a source row.
func (c *chunk) note(outcome string) { c.w.Count(outcome) }

// read records a source row the step has read.
func (m *Migration) read(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seen[key] = true
}

// tally records one consumed source row under outcome.
func (m *Migration) tally(key, outcome string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.handled[key] = true
	m.out[outcome]++
}

// memberKeyPrefix starts the synthetic key of a quarantined blob member
// ("setting:config_blob#<member>"): a derived record, not a source row.
const memberKeyPrefix = KeyBlob + "#"

// tallyQuarantine records a quarantined source row. A synthetic member key is not
// one, so it is not counted: the runner counts the quarantine record itself, and
// the accounting is over source rows.
func (m *Migration) tallyQuarantine(key string) {
	if strings.HasPrefix(key, memberKeyPrefix) {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.handled[key] = true
	m.quar++
}

// Step handles one row. A row is read once: by the visit the shard pays it, or by
// the visit of the group it belongs to.
func (m *Migration) Step(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
	return m.step(ctx, kv, w)
}

// step is Step over any writes, which Plan drives with its own.
func (m *Migration) step(ctx context.Context, kv storage.KV, w writes) error {
	c := &chunk{m: m, w: w}
	key := string(kv.Key)
	m.read(key)
	switch {
	case strings.HasPrefix(key, PrefixAIJobPayload):
		return c.quarantine(key, kv.Value, reasonOrphanPayload)
	case strings.HasPrefix(key, PrefixAIJobBatch):
		return c.quarantine(key, kv.Value, reasonOrphanBatch)
	case strings.HasPrefix(key, PrefixAIJob):
		return m.job(ctx, c, kv)
	case strings.HasPrefix(key, PrefixDirty):
		return c.consume(key, OutcomeDirty)
	case strings.HasPrefix(key, PrefixDailyBudget), strings.HasPrefix(key, PrefixThrottle):
		return c.consume(key, OutcomeHold)
	case key == KeyImportCounter, key == KeyBlob, strings.HasPrefix(key, PrefixImportPath):
		return m.group(ctx, c, kv)
	}
	return c.quarantine(key, kv.Value, reasonUnknownRow)
}

// get reads one legacy row, nil when absent.
func (m *Migration) get(ctx context.Context, key string) ([]byte, error) {
	raw, err := m.cfg.Legacy().Get(ctx, []byte(key))
	switch {
	case errors.Is(err, storage.ErrNotFound):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("m6: read %s: %w", key, err)
	}
	m.read(key)
	return bytes.Clone(raw), nil
}

// Verify is the step's invariant gate: nothing of any drained family is left, and
// this boot's accounting balances: every source row it read (visited or pulled
// forward) was consumed or quarantined, and nothing was consumed that it never
// read. Counts are this boot's: a boot that resumed after a crash reports only
// what it stepped.
func (m *Migration) Verify(ctx context.Context, r storage.Reader) (datamigrate.VerifyReport, error) {
	m.mu.Lock()
	rep := datamigrate.VerifyReport{Read: int64(len(m.seen)), Quarantined: m.quar}
	for _, n := range m.out {
		rep.Written += n
	}
	for _, key := range slices.Sorted(maps.Keys(m.seen)) {
		if !m.handled[key] {
			rep.Failures = append(rep.Failures, fmt.Sprintf("a row was read and neither consumed nor quarantined (%s)", key))
		}
	}
	for _, key := range slices.Sorted(maps.Keys(m.handled)) {
		if !m.seen[key] {
			rep.Failures = append(rep.Failures, fmt.Sprintf("a row was consumed that was never read (%s)", key))
		}
	}
	m.mu.Unlock()
	for _, rg := range SourceRanges() {
		for kv, err := range r.Scan(ctx, storage.ScanOptions{Start: rg.Start, End: rg.End, KeysOnly: true, Limit: 1}) {
			if err != nil {
				return rep, err
			}
			rep.Failures = append(rep.Failures, fmt.Sprintf("a row of a drained family is still present (%s)", kv.Key))
		}
	}
	return rep, nil
}
