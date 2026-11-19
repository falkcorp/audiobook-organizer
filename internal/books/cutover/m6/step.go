// file: internal/books/cutover/m6/step.go
// version: 1.0.1
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
	reasonOrphanPayload    = "the aijob_payload row has no aijob row"
	reasonOrphanBatch      = "the aijob_batch index row names no aijob row that holds it"
	reasonBlobUndecodable  = "the settings blob does not decode"
	reasonRootUndecodable  = "the import_path row does not decode"
	reasonRootConflict     = "core already holds a different root"
	reasonRefused          = "core's importer refused the value"
	reasonUnmappable       = "the iTunes mapping's To is under no library root"
	reasonUnknownRow       = "a row of a family M6 drains that no decoder reads"
	reasonUnprovenURL      = "a credential cannot be ruled out of the URL"
	reasonInstanceConflict = "core already holds a different instance under this ID"
	reasonAliasNoRoot      = "the path alias names no root M6 registers"
	reasonRootPath         = "the root path is not an absolute host path"
	reasonUnreadMember     = "a settings blob member that no part of M6 reads"
	reasonWebhookEvent     = "the webhook rule's events map to no topic"
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

	// DryRun makes Step read and plan but write nothing, staging no importer
	// call and deleting no row; the plan is recorded in Report (05-metadata
	// M1.10, "the step's dry-run mode").
	DryRun bool
	// Report receives one entry per instance, trust grant, root and quarantined
	// row, in both modes. nil keeps none.
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
	// pulled is the keys a dry run has consumed: a real run deletes them, so the
	// shard never reaches a row a group already took; a dry run deletes nothing,
	// so it must remember.
	pulled map[string]bool
	read   int64
	out    map[string]int64
	quar   int64
	count  map[string]int64
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
	case cfg.SecretNames == nil || cfg.SecretValue == nil:
		return datamigrate.DataMigration{}, errors.New("m6: Config.SecretNames and Config.SecretValue are required")
	case cfg.Legacy == nil:
		return datamigrate.DataMigration{}, errors.New("m6: Config.Legacy is books' legacy range")
	case id == "":
		return datamigrate.DataMigration{}, errors.New("m6: the step ID is required")
	case raisesLevel <= 0:
		return datamigrate.DataMigration{}, errors.New("m6: raisesLevel is the flip's books level, above 0")
	case len(after) == 0:
		return datamigrate.DataMigration{}, errors.New("m6: after is books' level step")
	}
	m := &Migration{cfg: cfg, by: id, logger: cfg.Logger, out: map[string]int64{}, count: map[string]int64{}, pulled: map[string]bool{}}
	if m.logger == nil {
		m.logger = slog.New(slog.DiscardHandler)
	}
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
		// The residual blob and a webhook family secret can hold a credential
		// when M2 could not move it, and a quarantined instance member is the
		// raw JSON of a source that may carry userinfo.
		Sensitive:       []string{KeyBlob, PrefixImportPath},
		RewritesInPlace: true,
		Step:            m.Step,
		Verify:          m.Verify,
	}, nil
}

// chunk is one Step call's writes: the StepWriter, or nothing in a dry run.
type chunk struct {
	m *Migration
	w *datamigrate.StepWriter
}

// batch is the chunk's batch, nil in a dry run (no importer is called then).
func (c *chunk) batch() *storage.Batch {
	if c.m.cfg.DryRun {
		return nil
	}
	return c.w.Batch()
}

// consume deletes a source row the step has handled and counts it under
// outcome. Every source row is consumed exactly once, whether Step was called
// for it or it was pulled forward, which is what Verify's balance relies on.
func (c *chunk) consume(key string, outcome string) error {
	if !c.m.cfg.DryRun {
		if err := c.w.Delete([]byte(key)); err != nil {
			return err
		}
		c.w.Count(outcome)
	} else {
		c.m.markPulled(key)
	}
	c.m.tally(outcome)
	return nil
}

// quarantine moves a source row (or a synthetic member key of it) aside with its
// raw bytes, deleting the source key, and counts it.
func (c *chunk) quarantine(key string, raw []byte, reason string) error {
	c.m.logger.Warn("m6: a row is quarantined", "key", key, "reason", reason)
	c.m.report(Entry{Kind: "quarantine", ID: key, Detail: reason})
	if !c.m.cfg.DryRun {
		if err := c.w.Quarantine([]byte(key), raw, reason); err != nil {
			return err
		}
	} else {
		c.m.markPulled(key)
	}
	c.m.tallyQuarantine(key)
	return nil
}

// note counts a derived outcome (an instance, a grant) that is not a source row.
func (c *chunk) note(outcome string) {
	if !c.m.cfg.DryRun {
		c.w.Count(outcome)
	}
	c.m.mu.Lock()
	c.m.count[outcome]++
	c.m.mu.Unlock()
}

func (m *Migration) markPulled(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pulled[key] = true
}

// alreadyTaken reports a row a dry run consumed with its group.
func (m *Migration) alreadyTaken(key string) bool {
	if !m.cfg.DryRun {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pulled[key]
}

// tally records one consumed source row under outcome.
func (m *Migration) tally(outcome string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.read++
	m.out[outcome]++
}

// tallyQuarantine records a quarantined key. A synthetic member key of the blob
// ("setting:config_blob#<member>") is a derived record, not a source row, so it
// is counted apart from the balance.
func (m *Migration) tallyQuarantine(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if strings.Contains(key, "#") {
		m.count["quarantined-member"]++
		return
	}
	m.read++
	m.quar++
}

// Counts is the step's own accounting of derived outcomes, for tests.
func (m *Migration) Counts() map[string]int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]int64{}
	for k, v := range m.count {
		out[k] = v
	}
	for k, v := range m.out {
		out[k] = v
	}
	return out
}

// Step handles one row. A row is read once: by the visit the shard pays it, or by
// the visit of the group it belongs to.
func (m *Migration) Step(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
	c := &chunk{m: m, w: w}
	key := string(kv.Key)
	if m.alreadyTaken(key) {
		return nil
	}
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
	return bytes.Clone(raw), nil
}

// Verify is the step's invariant gate: nothing of any drained family is left, and
// this boot's accounting balances (every source row consumed was imported,
// dropped by rule or quarantined, never neither). Counts are this boot's: a boot
// that resumed after a crash reports only what it stepped.
func (m *Migration) Verify(ctx context.Context, r storage.Reader) (datamigrate.VerifyReport, error) {
	m.mu.Lock()
	rep := datamigrate.VerifyReport{Read: m.read, Quarantined: m.quar}
	var handled int64
	for _, n := range m.out {
		handled += n
	}
	rep.Written = handled
	m.mu.Unlock()
	if m.cfg.DryRun {
		// A dry run deletes nothing, so the step must not be recorded as done: the
		// boot that rehearses it fails here, and a real boot runs it.
		rep.Failures = append(rep.Failures, "dry run: nothing was written and no row was deleted")
		return rep, nil
	}
	for _, rg := range SourceRanges() {
		for kv, err := range r.Scan(ctx, storage.ScanOptions{Start: rg.Start, End: rg.End, KeysOnly: true, Limit: 1}) {
			if err != nil {
				return rep, err
			}
			rep.Failures = append(rep.Failures, fmt.Sprintf("a row of a drained family is still present (%s)", kv.Key))
		}
	}
	if rep.Read != rep.Written+rep.Quarantined {
		rep.Failures = append(rep.Failures, fmt.Sprintf("read %d rows but handled %d and quarantined %d", rep.Read, rep.Written, rep.Quarantined))
	}
	return rep, nil
}
