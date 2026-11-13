// file: internal/plugins/maintenance/file_provenance_capture.go
// version: 1.3.0
// guid: 4f8e1a67-05b3-4d29-9c7e-3a6b2d80f514
// last-edited: 2026-11-13

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/appdirs"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/falkcorp/mediacore/tasks"
)

// fileProvenanceCaptureKind is rule R3: the native kind of maintenance.file-provenance-capture.
const fileProvenanceCaptureKind tasks.Kind = "books.maintenance.file-provenance-capture"

// Capturing provenance for files that are NOT yet in the library.
//
// The library's own files get a provenance event whenever the organizer writes
// tags to them. Files sitting outside the library have never been hashed by
// anything, so the first time we touch one — a copy, a move, a tag write — the
// pristine state is already gone. This op records it beforehand.
//
// Those events are orphans: there is no book_file row to attach them to yet.
// They are keyed by full-file SHA and adopted into a real chain by
// AdoptOrphanEvents once the file is imported.

// fileProvCaptureParams configures the capture sweep.
type fileProvCaptureParams struct {
	// Roots are the directories to walk. Required — there is deliberately no
	// default. A default would mean a mistyped or omitted parameter silently
	// starts a full-file SHA-256 over every mounted volume.
	Roots []string `json:"roots"`
	// Apply writes events. Default false: walk and hash nothing, just report
	// what would be captured.
	Apply bool `json:"apply"`
	// Max caps how many files are hashed in one run. Hashing is a full read of
	// every file, so an uncapped sweep over a NAS is a many-hour operation.
	Max int `json:"max"`
	// Extensions overrides which files are considered audio.
	Extensions []string `json:"extensions"`
}

const (
	fileProvDefaultMax = 500
	fileProvSampleCap  = 12
)

// fileProvDefaultExts is the op's default extension list when the caller
// passes none. It follows supported_extensions — this walk only hashes bytes,
// it decodes nothing — so a library holding .aax/.aiff/.mka/.oga/.wav books
// now gets provenance for them instead of a ledger that quietly covered part
// of the library while reporting a clean run.
func fileProvDefaultExts() []string { return config.SupportedExtensionSet().Sorted() }

// fileProvCaptureResult is what the op reports back.
type fileProvCaptureResult struct {
	Apply bool     `json:"apply"`
	Roots []string `json:"roots"`
	// Walked is every audio file seen under Roots.
	Walked int `json:"walked"`
	// Hashed is how many were actually read and digested. Zero on a dry run.
	Hashed int `json:"hashed"`
	// Recorded is how many produced a new ledger event.
	Recorded int `json:"recorded"`
	// AlreadyKnown is how many were skipped because the ledger already held an
	// identical observation, which is what makes re-running cheap and safe.
	AlreadyKnown int `json:"already_known"`
	// Capped is how many were left untouched because Max was reached. Reported
	// explicitly: a silent truncation reads as "we covered everything".
	Capped int `json:"capped"`
	// Errors is how many files could not be read.
	Errors int `json:"errors"`
	// Samples are a handful of concrete rows, for eyeballing a dry run.
	Samples []fileProvSample `json:"samples,omitempty"`
}

type fileProvSample struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256_full,omitempty"`
	Outcome   string `json:"outcome"`
}

func (p *Plugin) fileProvenanceCaptureDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.file-provenance-capture",
		Preview:     sdk.PreviewSpec{Params: fileProvCaptureParams{}},
		DisplayName: "Capture provenance for files outside the library",
		Description: "Walks the given roots, digests each audio file, and records an " +
			"append-only provenance event so the pristine hash exists before anything " +
			"copies, moves, or retags the file. Events are orphans until the file is " +
			"imported, then adopted into its book_file chain. Roots are required; " +
			"default dry-run, pass {\"apply\": true} to write events.",
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.file-provenance-capture",
		// ResumeDrop: this op writes. Re-running is safe and idempotent — an
		// already-recorded observation is detected and skipped — so dropping a
		// half-finished sweep costs progress, not correctness.
		ResumePolicy: sdk.ResumeDrop,
		Liveness:     sdk.LivenessRunItems,
		// CapLibraryWrite: no library row is touched, but this appends durable
		// records and a writing op must not claim read-only.
		Capabilities: []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run: func(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
			return p.runFileProvenanceCapture(ctx, raw, reporter)
		},
	}
}

// fileProvCaptureEnv is what the core needs from its engine (rules R2 and R10).
type fileProvCaptureEnv struct {
	t6g4Base
}

// fileProvCaptureNativeParams is fileProvCaptureParams for the native run: the
// same fields, except Apply is only a refusal marker (the mode comes from the
// start request; a live run applies, a preview walks and hashes nothing).
type fileProvCaptureNativeParams struct {
	Roots      []string `json:"roots"`
	Max        int      `json:"max"`
	Extensions []string `json:"extensions"`
	Apply      *bool    `json:"apply,omitempty"`
}

// fileProvenanceCaptureHeadEnv builds the env HEAD's shells give the core: the
// provenance store resolved off the deps (nil when no layer implements it).
func (p *Plugin) fileProvenanceCaptureHeadEnv(reporter sdk.Reporter, apply bool) fileProvCaptureEnv {
	prov := p.deps.FileProvenanceStore()
	return fileProvCaptureEnv{t6g4Base: t6g4HeadBase(reporter, apply,
		headT6G4Store{deps: p.deps, prov: prov}, headT6G4Writer{prov: prov})}
}

// runFileProvenanceCapture is the v2 shell around runFileProvenanceCaptureCore (rule R2 item 2).
func (p *Plugin) runFileProvenanceCapture(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	params, err := decodeFileProvCaptureParams(rawParams)
	if err != nil {
		return err
	}
	_, err = p.runFileProvenanceCaptureCore(ctx, params, p.fileProvenanceCaptureHeadEnv(reporter, params.Apply))
	return err
}

// fileProvenanceCaptureKind is the native shell (rule R2 item 3). HEAD has a
// preview, so there is no Live-only error.
func (p *Plugin) fileProvenanceCaptureKind(deps t6g4Deps, sched *tasks.Schedule) tasks.Definition {
	return tasks.Task(fileProvenanceCaptureKind, tasks.TaskSpec[fileProvCaptureNativeParams, fileProvCaptureResult]{
		Common: tasks.Common{
			Title: "Capture provenance for files outside the library",
			Help: "Walks the given roots, digests each audio file, and records an " +
				"append-only provenance event so the pristine hash exists before anything " +
				"copies, moves, or retags the file. Events are orphans until the file is " +
				"imported, then adopted into its book_file chain. Roots are required; " +
				"default dry-run, pass {\"apply\": true} to write events.",
			Effects:        tasks.Writes(t6g4Res("file_provenance")...),
			Perms:          tasks.Permissions{Start: t6g4StartRequirement()},
			Resume:         tasks.ResumeDrop,
			Priority:       tasks.PriorityLow,
			ConcurrencyKey: tasks.Key("books.maintenance.file-provenance-capture"),
			Schedule:       sched,
			Ready:          []tasks.ReadySignal{t6g4SignalMemDB},
		},
		Run: func(rc *tasks.RC, in fileProvCaptureNativeParams) (fileProvCaptureResult, error) {
			if in.Apply != nil {
				return fileProvCaptureResult{}, fmt.Errorf("%s: the apply param is not accepted; the run's mode comes from the start request (live applies, preview reports)", rc.Kind())
			}
			base, err := t6g4NativeBase(rc, deps, true)
			if err != nil {
				return fileProvCaptureResult{}, err
			}
			return p.runFileProvenanceCaptureCore(rc.Context(), fileProvCaptureParams{
				Roots: in.Roots, Apply: base.Live, Max: in.Max, Extensions: in.Extensions,
			}, fileProvCaptureEnv{t6g4Base: base})
		},
	})
}

// decodeFileProvCaptureParams is the params decode both HEAD entry points share.
func decodeFileProvCaptureParams(rawParams json.RawMessage) (fileProvCaptureParams, error) {
	var params fileProvCaptureParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fileProvCaptureParams{}, fmt.Errorf("file-provenance-capture: decode params: %w", err)
		}
	}
	return params, nil
}

// runFileProvenanceCaptureCore is HEAD's run body (rule R2 item 1): the sweep and
// the report lines.
func (p *Plugin) runFileProvenanceCaptureCore(ctx context.Context, params fileProvCaptureParams, env fileProvCaptureEnv) (fileProvCaptureResult, error) {
	log := env.Logger()
	result, err := p.captureFileProvenanceCore(ctx, params, env)
	if err != nil {
		return result, err
	}

	if b, mErr := json.Marshal(result); mErr == nil {
		log.Info("file-provenance-capture report (JSON)", "report", string(b))
	}
	if result.Capped > 0 {
		// Loud and explicit: a silent truncation reads as "everything is
		// captured", which is exactly the false confidence this op exists to
		// remove.
		log.Warn("file-provenance-capture: files left uncaptured by the cap — run again to continue",
			"uncaptured", result.Capped)
	}
	if result.Errors > 0 {
		log.Warn("file-provenance-capture: files skipped because they could not be read or recorded",
			"errors", result.Errors)
	}
	log.Info("file-provenance-capture complete",
		"apply", result.Apply, "walked", result.Walked, "hashed", result.Hashed,
		"recorded", result.Recorded, "already_known", result.AlreadyKnown,
		"capped", result.Capped, "errors", result.Errors)
	return result, nil
}

// captureFileProvenance does the work and returns the tally.
//
// Split from the reporter plumbing so the behaviour can be asserted directly
// rather than by parsing a log line back out of a fake reporter.
func (p *Plugin) captureFileProvenance(ctx context.Context, rawParams json.RawMessage) (fileProvCaptureResult, error) {
	params, err := decodeFileProvCaptureParams(rawParams)
	if err != nil {
		return fileProvCaptureResult{}, err
	}
	prov := p.deps.FileProvenanceStore()
	// No reporter: the sweep itself logs nothing, and the pinned callers assert
	// the tally directly.
	return p.captureFileProvenanceCore(ctx, params, fileProvCaptureEnv{t6g4Base: t6g4Base{
		Live: params.Apply, Store: headT6G4Store{deps: p.deps, prov: prov}, Writer: headT6G4Writer{prov: prov},
	}})
}

// captureFileProvenanceCore is the sweep itself over decoded params. The ledger
// is read through env.Store and appended to only through env.Writer.
func (p *Plugin) captureFileProvenanceCore(ctx context.Context, params fileProvCaptureParams, env fileProvCaptureEnv) (fileProvCaptureResult, error) {
	if len(params.Roots) == 0 {
		return fileProvCaptureResult{}, fmt.Errorf("file-provenance-capture: at least one root is required")
	}
	if params.Max <= 0 {
		params.Max = fileProvDefaultMax
	}
	exts := params.Extensions
	if len(exts) == 0 {
		exts = fileProvDefaultExts()
	}
	extSet := make(map[string]struct{}, len(exts))
	for _, e := range exts {
		extSet[strings.ToLower(e)] = struct{}{}
	}

	store := env.Store
	if !store.FileProvenanceAvailable() {
		return fileProvCaptureResult{}, fmt.Errorf("file-provenance-capture: provenance store not initialized")
	}

	result := fileProvCaptureResult{Apply: params.Apply, Roots: params.Roots}
	app := appdirs.Current()

	for _, root := range params.Roots {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if cerr := ctx.Err(); cerr != nil {
				return cerr
			}
			if err != nil {
				result.Errors++
				return nil // a single unreadable dir must not abort the sweep
			}
			if d.IsDir() {
				// Roots are operator-supplied, but that does not make what is
				// found BELOW one operator-supplied. This op HASHES every
				// matching file it walks, which is the named harm: pointed at
				// the library root it would hash multi-GB backup archives.
				//
				// ShouldSkipDir is still the right call for a caller-chosen
				// root: it exempts the root itself and any app dir CONTAINING
				// it, so deliberately capturing provenance for files under
				// `<root_dir>/openlibrary-dumps` still works when that
				// directory is named as the root. Only app dirs found inside a
				// wider tree are skipped.
				if pathutil.ShouldSkipDir(root, path, app) {
					return filepath.SkipDir
				}
				return nil
			}
			if _, ok := extSet[strings.ToLower(filepath.Ext(path))]; !ok {
				return nil
			}
			result.Walked++

			if result.Hashed >= params.Max {
				result.Capped++
				return nil
			}

			// d.Info() comes from the directory entry WalkDir already read, so
			// this needs no second path lookup.
			info, infoErr := d.Info()
			if infoErr != nil {
				result.Errors++
				return nil
			}

			if !params.Apply {
				// A dry run deliberately does NOT hash. Hashing is the whole
				// cost of this op, so a dry run that hashed would be as
				// expensive as the real thing and nobody would run it first.
				result.addSample(fileProvSample{Path: path, SizeBytes: info.Size(), Outcome: "would-capture"})
				return nil
			}

			sha, size, hashErr := fileops.ComputeFileHashAndSize(path)
			if hashErr != nil {
				result.Errors++
				return nil
			}
			result.Hashed++

			if known, kerr := alreadyObserved(store, sha, path); kerr == nil && known {
				result.AlreadyKnown++
				result.addSample(fileProvSample{Path: path, SizeBytes: size, SHA256: sha, Outcome: "already-known"})
				return nil
			}

			ev := database.FileEvent{
				Path: path,
				Kind: database.FileEventObserved,
				At:   time.Now(),
				Digest: database.FileDigest{
					SHA256Full: sha,
					SizeBytes:  size,
				},
				Actor:  "maintenance.file-provenance-capture",
				Detail: "captured outside the library, before any modification",
			}
			if aerr := env.Writer.AppendFileEvent(ev); aerr != nil {
				result.Errors++
				return nil
			}
			result.Recorded++
			result.addSample(fileProvSample{Path: path, SizeBytes: size, SHA256: sha, Outcome: "recorded"})
			return nil
		})
		if walkErr != nil {
			if ctx.Err() != nil {
				return result, walkErr
			}
			result.Errors++
		}
	}

	return result, nil
}

// fileEventFinder is the one ledger read alreadyObserved makes. It narrows the
// parameter from database.FileProvenanceStore (every caller still compiles) so
// the core can hand it env.Store.
type fileEventFinder interface {
	FindFileEventsByHash(hash string) ([]database.FileEvent, error)
}

// alreadyObserved reports whether the ledger already holds this exact
// observation — same content, same path. Re-running the sweep is then cheap and
// does not pile duplicate rows into an append-only store.
//
// The hash has already been computed by the caller, so this costs a prefix scan
// rather than another full read.
func alreadyObserved(store fileEventFinder, sha, path string) (bool, error) {
	events, err := store.FindFileEventsByHash(sha)
	if err != nil {
		return false, err
	}
	for _, e := range events {
		if e.Path == path && e.Kind == database.FileEventObserved {
			return true, nil
		}
	}
	return false, nil
}

func (r *fileProvCaptureResult) addSample(s fileProvSample) {
	if len(r.Samples) >= fileProvSampleCap {
		return
	}
	r.Samples = append(r.Samples, s)
}
