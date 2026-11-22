// file: internal/plugins/deluge/kinds.go
// version: 1.0.2
// guid: 907863ad-572b-4999-a2fe-90db4ad476a6
// last-edited: 2026-11-19

package deluge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/tagger"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// Native books Task kinds for the Deluge plugin (F5-05, 05-tasks 5T.8.2 group
// T5): the plugin's four v2 definitions, each as a tasks.Task kind named
// "books." + the HEAD def ID. They are DARK: nothing registers them before the
// tasks flip (F5-18), which also removes HEAD's v2 shells. No kinds*.go file
// imports pkg/plugin/sdk (the T5 gate); the sdk-facing half of the shells
// (HEAD's Reporter, the progress sink) is in env.go.
//
// Every definition has one shared core and two shells over it: HEAD's Run (the
// v2 shell, in the definition's own file) and the native tasks.Task below. A
// core reaches its stores only through the env it is handed (rule R10): reads
// through env.Store, writes through env.Writer, the Deluge client through the
// narrow interface its env declares (the client is not a store write and stays
// in the core), and the run (logging, progress, pools) through the hooks on
// kindEnv.

const (
	// signalMemDB is the readiness component every kind waits for (HEAD's
	// NoWarmupWait false).
	signalMemDB tasks.ReadySignal = "books.memdb"
)

// settingsManageScopes is the route requirement of HEAD's settings.manage atom
// (internal/books/atoms.go); the empty Permissions list of three of the four
// definitions defaults to it. internal/books imports this package through its
// import graph, so the table cannot be read from here; it is the copy
// internal/plugins/dedup and internal/plugins/maintenance keep (TestAtomTable_Golden
// in internal/books pins the original; kinds_test.go pins this copy against it).
var settingsManageScopes = []rbac.Permission{
	"core:settings.manage", "books:settings.manage", "core:tasks.manage", "core:system.view",
	"core:system.manage", "core:library.manage", "core:library.browse", "core:integrations.manage",
}

// integrationsManageScopes is the route requirement of the integrations.manage
// atom (internal/books/atoms.go): what auth.PermIntegrationsManage, HEAD's
// deluge.link-backfill Permissions, maps to. It is NOT settings.manage.
var integrationsManageScopes = []rbac.Permission{
	"core:integrations.manage", "books:integrations.manage",
}

// kindCommon is the Common block every definition shares; the caller fills the
// per-definition fields (Title, Help, Effects, Resume, Priority, Timeout...) and
// Perms when the requirement is not settings.manage.
func kindCommon(kind tasks.Kind, sched func(tasks.Kind) *tasks.Schedule) tasks.Common {
	c := tasks.Common{
		Perms: tasks.Permissions{Start: rbac.AllOf(settingsManageScopes...)},
		Ready: []tasks.ReadySignal{signalMemDB}, // HEAD's NoWarmupWait is false for all four
	}
	if sched != nil {
		c.Schedule = sched(kind)
	}
	return c
}

// res names books resources for an Effects value, from the pkg/ops constants
// only (a literal name is a drift the resource-vocabulary test cannot see).
func res(rs ...ops.Resource) []entity.Ref {
	out := make([]entity.Ref, len(rs))
	for i, r := range rs {
		out[i] = tasks.Res("books", string(r))
	}
	return out
}

// kindModeKeys are the param keys that would carry the run's mode: dry_run,
// dryRun and apply. The mode comes from the start request (05-tasks 5T.3.7),
// never from params, so a native shell refuses them rather than dropping them
// silently: tasks decodes native params with plain json.Unmarshal, and a
// dropped {"dry_run":true} would turn a dry-run start into a live write.
var kindModeKeys = []string{"dry_run", "dryRun", "apply"}

// decodeKindParams is the native shells' params step: a mode key is refused
// (case-insensitively, as encoding/json matches field names), then the rest is
// decoded into v. Empty and null params decode to the zero value; params that
// are not a JSON object are refused.
func decodeKindParams(kind tasks.Kind, raw json.RawMessage, v any) error {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || bytes.Equal(t, []byte("null")) {
		return nil
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(t, &keys); err != nil {
		return fmt.Errorf("%s: decode params: %w", kind, err)
	}
	for key := range keys {
		for _, banned := range kindModeKeys {
			if strings.EqualFold(key, banned) {
				return fmt.Errorf("%s: params carry %q; the run's mode comes from the start request, not params", kind, key)
			}
		}
	}
	if err := json.Unmarshal(t, v); err != nil {
		return fmt.Errorf("%s: decode params: %w", kind, err)
	}
	return nil
}

// decodeNativeParams is decodeKindParams into a fresh P.
func decodeNativeParams[P any](kind tasks.Kind, raw json.RawMessage) (P, error) {
	var v P
	err := decodeKindParams(kind, raw, &v)
	return v, err
}

// refuseKindModeKeys is decodeKindParams for a shell whose body takes no params
// of its own; any other key is ignored, as HEAD ignored the params of these defs.
func refuseKindModeKeys(kind tasks.Kind, raw json.RawMessage) error {
	return decodeKindParams(kind, raw, &struct{}{})
}

// kindLiveOnly is the refusal a live-only writer gives a Preview run: HEAD has
// no preview for it, and a native Preview must not write.
func kindLiveOnly(rc *tasks.RC, kind tasks.Kind) error {
	if rc.Mode() != tasks.ModeLive {
		return fmt.Errorf("%s: preview not supported; start with mode live", kind)
	}
	return nil
}

// kindStore is the reads every core takes, composed by embedding each
// definition's own narrow interface (the F5-11 precedent): no aggregate
// interface is declared here. The embedded method sets overlap with identical
// signatures.
type kindStore interface {
	pathUpdateStore
	centralizeStore
	linkReadStore
}

// kindWriter is the writes the cores make, composed the same way.
type kindWriter interface {
	centralizeWriter
	linkWriter
}

// delugeClient is the Deluge calls the cores make, composed the same way. None
// of them is a store write.
type delugeClient interface {
	storageMover
	libraryImporter
	torrentLister
}

// protectedCache is the plugin's protected-path cache as the cores use it:
// protected-paths-sync refreshes it, centralize asks it whether a path is
// protected.
type protectedCache interface {
	protectedRefresher
	tagger.PathChecker
}

// KindDeps is what the native kinds need from their owner. Nothing passes it in
// production before F5-18, which wires the store, the gated writer
// (internal/books/writer behind NewWriter), the Deluge client and the cache;
// tests pass fakes.
type KindDeps struct {
	// Store is the reads; the writes go through NewWriter only. A store that
	// also exposes database.FileProvenanceStore (through any decorator chain)
	// enables deluge.link-backfill's provenance rule, as at HEAD.
	Store kindStore
	// NewWriter builds a run's writer over its kernel-gated tasks.Writer. It is
	// called only by a Live run of a kind that writes the store.
	NewWriter func(tasks.Writer) kindWriter
	// Client is the Deluge client, nil when Deluge is not configured
	// (centralize then imports without moving torrent storage, as at HEAD).
	Client delugeClient
	// Cache is the protected-path cache; nil is allowed only for centralize
	// (only the configured static prefixes then protect a path, as at HEAD).
	Cache protectedCache
	// Config is the live configuration (HEAD read config.AppConfig).
	Config *config.Config

	// LinkHash, LinkNow and LinkRoundFiles are deluge.link-backfill's test
	// seams (Plugin.linkHash, linkNow, linkRoundFiles at HEAD); unset, the
	// production defaults apply (filehash.BookFileHash, time.Now,
	// linkRoundHashFiles).
	LinkHash       func(path string) (string, error)
	LinkNow        func() time.Time
	LinkRoundFiles int
}

// writerFor builds the run's writer over its kernel-gated tasks.Writer,
// refusing a run whose owner declared no factory (or one that returned nothing).
func writerFor(kind tasks.Kind, d KindDeps, rc *tasks.RC) (kindWriter, error) {
	if err := errNeed(kind, "NewWriter", d.NewWriter != nil); err != nil {
		return nil, err
	}
	w := d.NewWriter(rc.Writer())
	if w == nil {
		return nil, errNeedWriter(kind)
	}
	return w, nil
}

// Kinds returns every native kind this plugin declares, in HEAD's
// OperationDefs order, with Schedule: sched(kind) on each (rule R9; a nil sched
// means no schedule). Nothing calls it in production before F5-18.
func (p *Plugin) Kinds(deps KindDeps, sched func(tasks.Kind) *tasks.Schedule) []tasks.Definition {
	return kindList(p, deps, sched)
}

// kindList is Kinds as a package-level function: tasklint's unregistered-kind
// rule counts package-level list functions only, and not a method.
func kindList(p *Plugin, deps KindDeps, sched func(tasks.Kind) *tasks.Schedule) []tasks.Definition {
	return []tasks.Definition{
		p.protectedPathsSyncNative(deps, sched),
		p.centralizeNative(deps, sched),
		p.pathUpdateNative(deps, sched),
		p.linkBackfillNative(deps, sched),
	}
}

// kindListRoot names kindList from non-test code so tasklint's
// unregistered-kind rule counts it a registration root (a list function nothing
// references registers nothing, and the analyzer does not load test files).
// F5-18 deletes it with the HEAD shells.
var _ = kindList

// The brief's list-function line, kept so the method stays referenced.
var _ = (*Plugin).Kinds

// protectedPathsSyncNative is the native deluge.protected-paths-sync kind.
func (p *Plugin) protectedPathsSyncNative(d KindDeps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := kindCommon(protectedPathsSyncKind, sched)
	c.Title = "Sync protected paths from Deluge"
	c.Help = protectedPathsSyncHelp
	// ReadOnly found: the body refreshes an in-memory cache from Deluge's
	// torrent list and writes no store row and no file.
	c.Effects = tasks.ReadOnly()
	// HEAD Requeue: FromZero. The body keeps no checkpoint.
	c.Resume = tasks.ResumeFromZero
	c.Priority = tasks.PriorityLow
	c.ConcurrencyKey = tasks.Key("books.deluge.protected-paths-sync")
	c.Timeout = 5 * time.Minute
	return tasks.Task(protectedPathsSyncKind, tasks.TaskSpec[json.RawMessage, protectedPathsSyncResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (protectedPathsSyncResult, error) {
			if err := refuseKindModeKeys(protectedPathsSyncKind, raw); err != nil {
				return protectedPathsSyncResult{}, err
			}
			env := protectedPathsSyncEnv{kindEnv: nativeEnv(rc)}
			// An untyped nil unless the owner set one (a typed nil would pass
			// the core's check and fail inside Refresh).
			if d.Cache != nil {
				env.Cache = d.Cache
			}
			return protectedPathsSyncCore(rc.Context(), env)
		},
	})
}

// centralizeNative is the native deluge.centralize kind. HEAD has no preview
// (no params, one write pass), so a native Preview is refused.
func (p *Plugin) centralizeNative(d KindDeps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := kindCommon(centralizeKind, sched)
	c.Title = "Centralize Deluge books"
	c.Help = centralizeHelp
	// Writes found (HEAD declares none): the core reaches the writer's
	// ModifyBookFile (delugeclient.MarkImportedInPlace) and UpdateBookFile
	// (delugeclient.ImportToLibraryWith's repoint). Both are the shared table's
	// fileRow (internal/maintenance/resources): the book_files row plus the
	// books aggregate it recomputes (books, authors, metadata_cache, indexes).
	// ImportToLibraryWith also copies the file's bytes into the library and
	// asks Deluge to move the torrent's storage: files.
	c.Effects = tasks.Writes(res(ops.ResBookFiles, ops.ResBooks, ops.ResAuthors,
		ops.ResMetadataCache, ops.ResIndexes, ops.ResFiles)...)
	// HEAD Restart: FromZero. The counts-only checkpoint is liveness, not a
	// resume position (centralizationCheckpoint's doc): the body never reads
	// it, and a re-run lists exactly the rows still unimported.
	c.Resume = tasks.ResumeFromZero
	c.Priority = tasks.PriorityNormal
	c.ConcurrencyKey = tasks.Key("books.deluge.centralize")
	c.Timeout = 24 * time.Hour
	return tasks.Task(centralizeKind, tasks.TaskSpec[json.RawMessage, centralizeResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (centralizeResult, error) {
			if err := refuseKindModeKeys(centralizeKind, raw); err != nil {
				return centralizeResult{}, err
			}
			if err := kindLiveOnly(rc, centralizeKind); err != nil {
				return centralizeResult{}, err
			}
			if err := errNeed(centralizeKind, "Store", d.Store != nil); err != nil {
				return centralizeResult{}, err
			}
			if err := errNeed(centralizeKind, "Config", d.Config != nil); err != nil {
				return centralizeResult{}, err
			}
			w, err := writerFor(centralizeKind, d, rc)
			if err != nil {
				return centralizeResult{}, err
			}
			env := centralizeEnv{
				kindEnv: nativeEnv(rc),
				Cfg:     d.Config,
				Store:   d.Store,
				Writer:  w,
				// HEAD handed ImportToLibraryWith a possibly nil *Client, which it
				// reads as "never move storage": the zero adapter is that.
				Importer: headDelugeClient{},
			}
			if d.Client != nil {
				env.Importer = d.Client
			}
			if d.Cache != nil {
				env.Protected = d.Cache
			}
			return centralizeCore(rc.Context(), env)
		},
	})
}

// pathUpdateNative is the native deluge.path-update kind. Preview is HEAD's dry
// run (dry_run defaulted to true there, and a native run that names no mode
// resolves to Preview for a writer kind, so the default carries over).
func (p *Plugin) pathUpdateNative(d KindDeps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := kindCommon(pathUpdateKind, sched)
	c.Title = "Update Deluge torrent path"
	c.Help = pathUpdateHelp
	// Writes found (HEAD declares none, only the CapFilesWrite capability): a
	// live run asks Deluge for core.move_storage, which relocates the torrent's
	// bytes on disk. The core writes no store row, so the one resource is files.
	// This is what makes the kind a writer (default mode Preview, as HEAD's
	// dry_run default): a ReadOnly kind would default to Live and move torrents
	// for a start that named no mode.
	c.Effects = tasks.Writes(res(ops.ResFiles)...)
	// HEAD Drop: the body keeps no checkpoint and the event that would fire it
	// again is not carried.
	c.Resume = tasks.ResumeDrop
	c.Priority = tasks.PriorityNormal
	c.ConcurrencyKey = tasks.Key("books.deluge.path-update")
	// Fold a start only into a QUEUED run of the same book (F6-01 obligation
	// 3, for the relocation consumer). The zero policy would also fold it into
	// a RUNNING run with identical params, which may already have read the old
	// path: the newer move would be lost. A start made while one runs queues
	// behind it, and reads the path as stored when it runs.
	c.Dedupe = tasks.DedupePolicy{Key: func(raw json.RawMessage) (string, error) {
		params, err := decodeNativeParams[pathUpdateCoreParams](pathUpdateKind, raw)
		return params.BookID, err
	}}
	c.Timeout = 1 * time.Minute
	return tasks.Task(pathUpdateKind, tasks.TaskSpec[json.RawMessage, pathUpdateResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (pathUpdateResult, error) {
			args, err := decodeNativeParams[pathUpdateCoreParams](pathUpdateKind, raw)
			if err != nil {
				return pathUpdateResult{}, err
			}
			if err := errNeed(pathUpdateKind, "Store", d.Store != nil); err != nil {
				return pathUpdateResult{}, err
			}
			if err := errNeed(pathUpdateKind, "Config", d.Config != nil); err != nil {
				return pathUpdateResult{}, err
			}
			env := pathUpdateEnv{kindEnv: nativeEnv(rc), Cfg: d.Config, Store: d.Store}
			if d.Client != nil {
				env.Mover = d.Client
			}
			return pathUpdateCore(rc.Context(), args, env)
		},
	})
}

// linkBackfillNative is the native deluge.link-backfill kind. Preview is HEAD's
// dry run (the op's documented default).
func (p *Plugin) linkBackfillNative(d KindDeps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := kindCommon(linkBackfillKind, sched)
	c.Title = "Link Deluge torrents to library files"
	c.Help = linkBackfillHelp
	// HEAD declares Writes [ResBookFiles]; that is a subset of what the body
	// writes. The only write is linkWriter.ModifyBookFile (link_backfill.go:1776,
	// linkRun.link), and the store's ModifyBookFile recomputes the book's
	// aggregates after it commits the file row (store.PebbleStore.ModifyBookFile
	// -> notifyBookFileChange -> RecomputeBookAggregates -> ModifyBook), so the
	// shared table's row for it is book_files plus the books row, its
	// metadata_cache stamp and its ident rows (indexes). The kind declares that
	// row: the fence refuses a write whose row is not inside the declaration, so
	// HEAD's narrower set would break the backfill once the executor fences it.
	// HEAD has no fence and already performs these writes; this declares them.
	c.Effects = tasks.Writes(res(ops.ResBookFiles, ops.ResBooks, ops.ResIndexes, ops.ResMetadataCache)...)
	// R6: HEAD's Permissions [auth.PermIntegrationsManage] through the atom table.
	c.Perms = tasks.Permissions{Start: rbac.AllOf(integrationsManageScopes...)}
	// HEAD Restart: FromZero. The body keeps no checkpoint; a linked row is
	// skipped as already_linked, so a re-run is idempotent.
	c.Resume = tasks.ResumeFromZero
	c.Priority = tasks.PriorityNormal
	c.ConcurrencyKey = tasks.Key("books.deluge.link-backfill")
	c.Timeout = 4 * time.Hour
	return tasks.Task(linkBackfillKind, tasks.TaskSpec[json.RawMessage, linkBackfillResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (linkBackfillResult, error) {
			args, err := decodeNativeParams[linkBackfillCoreParams](linkBackfillKind, raw)
			if err != nil {
				return linkBackfillResult{}, err
			}
			if err := errNeed(linkBackfillKind, "Store", d.Store != nil); err != nil {
				return linkBackfillResult{}, err
			}
			if err := errNeed(linkBackfillKind, "Config", d.Config != nil); err != nil {
				return linkBackfillResult{}, err
			}
			env := linkBackfillEnv{
				kindEnv:      nativeEnv(rc),
				Store:        d.Store,
				DefaultLabel: d.Config.DelugeDiscoveryLabel,
				Root:         d.Config.RootDir,
				Hash:         d.LinkHash,
				Now:          d.LinkNow,
				RoundFiles:   d.LinkRoundFiles,
			}
			if d.Client != nil {
				env.Client = d.Client
			}
			if prov, ok := database.AsCapability[database.FileProvenanceStore](d.Store); ok {
				env.Prov = prov
			}
			if env.Live {
				w, err := writerFor(linkBackfillKind, d, rc)
				if err != nil {
					return linkBackfillResult{}, err
				}
				env.Writer = w
			}
			return linkBackfillCore(rc.Context(), args, env)
		},
	})
}
