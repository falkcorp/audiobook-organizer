// file: internal/scheduler/extra_ops.go
// version: 1.32.0
// guid: a9b8c7d6-e5f4-3210-fedc-ba9876543210
// last-edited: 2026-11-18

// extra_ops registers the OperationDef for the scheduler task that previously
// used the legacy triggerOperation / triggerOperationWithID helpers.  The def
// extracts the closure logic into a typed Run func so the UOS v2 dispatcher
// can manage lifecycle, cancellation, and concurrency without the old
// BridgeQueue.
//
// Extracted from internal/server/scheduler_extra_ops.go (SERVER-THIN-RESIDUAL).
// Receiver changed from *Server to *ExtraOpsRegistrar.

package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// ExtraOpsDeps holds the typed dependencies needed by ExtraOpsRegistrar.
// All fields are optional (nil-safe) to match the server's lazy-init pattern.
type ExtraOpsDeps struct {
	MetadataFetchService *metafetch.Service
	// ScanStandDown is the scan stand-down gate the metadata upgrade op
	// holds while it applies (metadata is never applied during a library scan).
	// Nil = ungated (tests).
	ScanStandDown opsregistry.ScanStandDownGate
}

// ExtraOpsRegistrar holds a Store reference and typed Deps so the scheduler
// OperationDef can be registered without a *Server pointer.
type ExtraOpsRegistrar struct {
	Deps  ExtraOpsDeps
	Store metabatch.Store
}

// NewExtraOpsRegistrar constructs an ExtraOpsRegistrar.
func NewExtraOpsRegistrar(store metabatch.Store, deps ExtraOpsDeps) *ExtraOpsRegistrar {
	return &ExtraOpsRegistrar{Store: store, Deps: deps}
}

// --- metadata-upgrade ---

// RegisterMetadataUpgradeOp registers the scheduler.metadata-upgrade OperationDef.
//
// It is the ONLY registration of the metadata upgrade (2026-09-27). Until then
// the same job was also registered as maintenance.metadata-upgrade by the
// maintenance plugin, a second def with its own ConcurrencyKey, so the two
// could run at once over the same books. Nothing in web/src or the handlers
// named the maintenance ID; it stays reachable as a FormerID, so an old
// operations_v2 row or an API enqueue by that ID resolves to this def.
func (r *ExtraOpsRegistrar) RegisterMetadataUpgradeOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(r.metadataUpgradeDef())
}

// metadataUpgradeOpID is the id of the metadata upgrade definition.
const metadataUpgradeOpID = "scheduler.metadata-upgrade"

// metadataUpgradeDef is HEAD's definition of scheduler.metadata-upgrade. Its Run
// is the v2 shell over metadataUpgradeCore; the native books.* kind is
// metadataUpgradeNative.
func (r *ExtraOpsRegistrar) metadataUpgradeDef() opsregistry.OperationDef {
	return opsregistry.OperationDef{
		ID:              metadataUpgradeOpID,
		FormerIDs:       []string{"maintenance.metadata-upgrade"},
		Liveness:        opsregistry.LivenessManual,
		Plugin:          "scheduler",
		DisplayName:     "Metadata Upgrade",
		Description:     "Replace metadata from lower-ranked sources (Open Library, Google Books, Wikipedia) with a higher-ranked source's (Hardcover, Audible) when its match passes the bulk-apply gate. User-locked fields are kept.",
		DefaultPriority: opsregistry.PriorityLow,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         4 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  "scheduler.metadata-upgrade",
		Permissions:     []auth.Permission{auth.PermSettingsManage},
		// Audible and generic network: the search queries every metadata
		// provider. OpenAI: the search may LLM-rerank its candidates.
		Capabilities: []opsregistry.Capability{
			opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite,
			opsregistry.CapNetworkAudible, opsregistry.CapNetworkGeneric, opsregistry.CapNetworkOpenAI,
		},
		Run: func(ctx context.Context, _ json.RawMessage, reporter opsregistry.Reporter) error {
			_, err := metadataUpgradeCore(ctx, metadataUpgradeEnv{
				t7Env:     t7HeadEnv(reporter),
				Store:     headMetadataUpgrade{r: r},
				Writer:    headMetadataUpgrade{r: r},
				StandDown: t7HeadStandDown(r.Deps.ScanStandDown, reporter),
			})
			return err
		},
	}
}

// metadataUpgradeStore is the reads of scheduler.metadata-upgrade: whether the
// metadata fetch service the sweep runs over exists.
type metadataUpgradeStore interface {
	// Ready reports whether the upgrade can run at all (a metadata fetch service
	// is wired).
	Ready() error
}

// metadataUpgradeWriter is the writes of scheduler.metadata-upgrade: the upgrade
// sweep itself (re-fetch each eligible book's candidates and apply the winner's
// metadata through the bulk-apply gate; metabatch.MetadataUpgradeService).
type metadataUpgradeWriter interface {
	// RunUpgrade sweeps up to limit books, reporting progress as it goes.
	RunUpgrade(ctx context.Context, limit int, progress operations.ProgressReporter) (*metabatch.UpgradeResult, error)
}

// metadataUpgradeEnv is what the shared core takes from its shell (rules R2, R10).
type metadataUpgradeEnv struct {
	t7Env
	Store  metadataUpgradeStore
	Writer metadataUpgradeWriter
	// StandDown holds the scan stand-down for the run: metadata is never applied
	// during a library scan.
	StandDown t7StandDown
}

// metadataUpgradeCore is the body of scheduler.metadata-upgrade, shared by HEAD's
// shell and the native kind. It returns the sweep's result (nil when the sweep did
// not run).
func metadataUpgradeCore(ctx context.Context, env metadataUpgradeEnv) (res *metabatch.UpgradeResult, retErr error) {
	// Metadata is never applied during a library scan: hold the scan
	// stand-down before the first write. A running scan does not make
	// the op refuse: the acquire quiesces it and waits for it to park
	// (bounded by the lease), and the op fails only if it never parks.
	hold, sdErr := env.StandDown(ctx, "scheduler.metadata-upgrade apply")
	if sdErr != nil {
		return nil, sdErr
	}
	defer func() { retErr = hold.Finish(retErr) }()
	ctx, env.t7Env = t7Rebind(ctx, env.t7Env, hold)

	progress := env.reporter()
	p := sdk.NewProgress(progress, 0)
	p.Start("Scanning for books with upgradeable metadata sources...")
	if err := env.Store.Ready(); err != nil {
		return nil, err
	}
	_ = progress.Log("info", "Scanning for books with upgradeable metadata sources...", nil)
	// M7 (2026-07 error-correction sweep): thread progress through so
	// this 4-hour-timeout op reports books processed/total every 25
	// books instead of going silent between start and result.
	result, err := env.Writer.RunUpgrade(ctx, 200, progress)
	if err != nil {
		return nil, err
	}
	msg := fmt.Sprintf("Metadata upgrade complete: checked %d, upgraded %d, skipped %d, "+
		"owner-manual-only %d (Doctor Who / Big Finish / Torchwood), errors %d; sweep after %q -> %q (wrapped %v)",
		result.Checked, result.Upgraded, result.Skipped, result.OwnerManualOnly, result.Errors,
		result.CursorStart, result.CursorEnd, result.Wrapped)
	_ = progress.Log("info", msg, nil)
	p.Done(msg)
	return result, nil
}

// metadataUpgradeNative is the native books.scheduler.metadata-upgrade kind. HEAD
// declares no Writes, no Preview and a library write capability, so a Preview run
// is refused. Its write set is inferred from what the sweep writes: the books it
// applies metadata to (with their authors and series and the metadata cache) and
// the sweep cursor it saves as operation state.
func (r *ExtraOpsRegistrar) metadataUpgradeNative(deps t7Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	kind := t7KindFor(metadataUpgradeOpID)
	c := t7Common(r.metadataUpgradeDef(), sched)
	c.Effects = t7Writes(ops.ResBooks, ops.ResAuthors, ops.ResSeries, ops.ResMetadataCache, ops.ResOperations)
	return tasks.Task(kind, tasks.TaskSpec[json.RawMessage, metabatch.UpgradeResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (metabatch.UpgradeResult, error) {
			if err := t7LiveOnly(rc, kind); err != nil {
				return metabatch.UpgradeResult{}, err
			}
			// HEAD's body takes no params; a mode key is still refused.
			if _, err := t7DecodeNative[struct{}](kind, raw); err != nil {
				return metabatch.UpgradeResult{}, err
			}
			if err := t7NeedStore(kind, deps); err != nil {
				return metabatch.UpgradeResult{}, err
			}
			w, err := t7NewWriter(kind, deps, rc)
			if err != nil {
				return metabatch.UpgradeResult{}, err
			}
			res, err := metadataUpgradeCore(rc.Context(), metadataUpgradeEnv{
				t7Env:     t7NativeEnv(rc),
				Store:     deps.Store,
				Writer:    w,
				StandDown: t7NativeStandDown(rc, deps.StandDownKey),
			})
			if res == nil {
				return metabatch.UpgradeResult{}, err
			}
			return *res, err
		},
	})
}

// headMetadataUpgrade is HEAD's side of the store and the writer: the fetch service the
// registrar was built with and its store, passed to the upgrade as HEAD's body
// passed them.
type headMetadataUpgrade struct{ r *ExtraOpsRegistrar }

func (h headMetadataUpgrade) Ready() error {
	if h.r.Deps.MetadataFetchService == nil {
		return fmt.Errorf("metadata fetch service not initialized")
	}
	return nil
}

func (h headMetadataUpgrade) RunUpgrade(ctx context.Context, limit int, progress operations.ProgressReporter) (*metabatch.UpgradeResult, error) {
	svc := metabatch.NewMetadataUpgradeService(h.r.Store, h.r.Deps.MetadataFetchService)
	return svc.RunUpgrade(ctx, limit, progress)
}
