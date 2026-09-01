// file: internal/plugins/maintenance/file_provenance_capture.go
// version: 1.2.0
// guid: 4f8e1a67-05b3-4d29-9c7e-3a6b2d80f514
// last-edited: 2026-09-01

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
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

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

func (p *Plugin) runFileProvenanceCapture(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	log := reporter.Logger()
	result, err := p.captureFileProvenance(ctx, rawParams)
	if err != nil {
		return err
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
	return nil
}

// captureFileProvenance does the work and returns the tally.
//
// Split from the reporter plumbing so the behaviour can be asserted directly
// rather than by parsing a log line back out of a fake reporter.
func (p *Plugin) captureFileProvenance(ctx context.Context, rawParams json.RawMessage) (fileProvCaptureResult, error) {
	var params fileProvCaptureParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fileProvCaptureResult{}, fmt.Errorf("file-provenance-capture: decode params: %w", err)
		}
	}
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

	store := p.deps.FileProvenanceStore()
	if store == nil {
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
			if aerr := store.AppendFileEvent(ev); aerr != nil {
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

// alreadyObserved reports whether the ledger already holds this exact
// observation — same content, same path. Re-running the sweep is then cheap and
// does not pile duplicate rows into an append-only store.
//
// The hash has already been computed by the caller, so this costs a prefix scan
// rather than another full read.
func alreadyObserved(store database.FileProvenanceStore, sha, path string) (bool, error) {
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
