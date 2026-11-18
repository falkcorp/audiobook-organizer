// file: internal/server/openlibrary_ops.go
// version: 1.4.0
// guid: 3c7e9a21-f4b5-4d68-8e2f-1a6c0b9d7f43
// last-edited: 2026-11-18

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/openlibrary"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

const (
	olDownloadOpID = "openlibrary.download"
	olImportOpID   = "openlibrary.import"
)

type olDownloadOpParams struct {
	Types     []string `json:"types"`
	TargetDir string   `json:"target_dir"`
}

type olImportOpParams struct {
	Types     []string `json:"types"`
	TargetDir string   `json:"target_dir"`
}

// olDownloadWriter is the one write the dump download makes: a dump file under
// the target directory.
type olDownloadWriter interface {
	DownloadOLDump(dumpType, targetDir string) error
}

// olImportWriter is the writes of the dump import: opening the local search
// store under the target directory, and loading the dumps into it.
type olImportWriter interface {
	EnsureOLStore(targetDir string) error
	ImportOLDumps(ctx context.Context, progress operations.ProgressReporter, targetDir string, types []string) error
}

// olDownloadEnv is what the shared download core takes from its shell.
type olDownloadEnv struct {
	t7Env
	Writer olDownloadWriter
}

// olDownloadCore is the body of openlibrary.download, shared by HEAD's shell and
// the native kind.
func olDownloadCore(p olDownloadOpParams, env olDownloadEnv) error {
	if env.Writer == nil {
		return fmt.Errorf("openlibrary.download: OpenLibrary service not initialized")
	}
	for i, dumpType := range p.Types {
		if env.Canceled != nil && env.Canceled() {
			return fmt.Errorf("download canceled")
		}
		env.Log(slog.LevelInfo, fmt.Sprintf("Starting OL dump download: %s", dumpType))
		env.progress(i, len(p.Types), fmt.Sprintf("Downloading %s...", dumpType))
		if err := env.Writer.DownloadOLDump(dumpType, p.TargetDir); err != nil {
			env.Log(slog.LevelError, fmt.Sprintf("OL dump download failed for %s: %v", dumpType, err))
			return fmt.Errorf("download failed for %s: %w", dumpType, err)
		}
		env.Log(slog.LevelInfo, fmt.Sprintf("OL dump download complete: %s", dumpType))
	}
	env.progress(len(p.Types), len(p.Types), "All downloads complete")
	return nil
}

// olImportEnv is what the shared import core takes from its shell.
type olImportEnv struct {
	t7Env
	Writer olImportWriter
}

// olImportCore is the body of openlibrary.import, shared by HEAD's shell and the
// native kind.
func olImportCore(ctx context.Context, p olImportOpParams, env olImportEnv) error {
	if env.Writer == nil {
		return fmt.Errorf("openlibrary.import: OpenLibrary service not initialized")
	}
	if err := env.Writer.EnsureOLStore(p.TargetDir); err != nil {
		return fmt.Errorf("failed to open store: %w", err)
	}
	return env.Writer.ImportOLDumps(ctx, env.reporter(), p.TargetDir, p.Types)
}

// headOLDownloadWriter is HEAD's download write: openlibrary.DownloadDump with
// the service's tracker, as HEAD's body called it.
type headOLDownloadWriter struct{ tracker *openlibrary.DownloadTracker }

func (w headOLDownloadWriter) DownloadOLDump(dumpType, targetDir string) error {
	return openlibrary.DownloadDump(dumpType, targetDir, w.tracker)
}

// headOLImportWriter is HEAD's import writes: the service's EnsureStore then
// Import.
type headOLImportWriter struct{ svc *metafetch.OpenLibraryService }

func (w headOLImportWriter) EnsureOLStore(targetDir string) error {
	return w.svc.EnsureStore(targetDir)
}

func (w headOLImportWriter) ImportOLDumps(ctx context.Context, progress operations.ProgressReporter, targetDir string, types []string) error {
	return w.svc.Import(ctx, progress, targetDir, types)
}

// olDownloadDef is HEAD's openlibrary.download definition (the v2 shell).
func (s *Server) olDownloadDef() opsregistry.OperationDef {
	return opsregistry.OperationDef{
		ID:              olDownloadOpID,
		Liveness:        opsregistry.LivenessManual,
		Plugin:          "openlibrary",
		DisplayName:     "OpenLibrary Dump Download",
		Description:     "Download OpenLibrary data dump files (editions, authors, works).",
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         8 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  olDownloadOpID,
		Permissions:     []auth.Permission{auth.PermSettingsManage},
		Capabilities:    []opsregistry.Capability{opsregistry.CapNetworkOpenLibrary, opsregistry.CapFilesWrite},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			var p olDownloadOpParams
			if len(rawParams) > 0 {
				if err := json.Unmarshal(rawParams, &p); err != nil {
					return fmt.Errorf("openlibrary.download: decode params: %w", err)
				}
			}
			env := olDownloadEnv{t7Env: t7HeadEnv(reporter, true)}
			if s.olService != nil {
				env.Writer = headOLDownloadWriter{tracker: s.olService.Tracker}
			}
			return olDownloadCore(p, env)
		},
	}
}

// RegisterOLDownloadOp registers the "openlibrary.download" v2 OperationDef.
func (s *Server) RegisterOLDownloadOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(s.olDownloadDef())
}

// olImportDef is HEAD's openlibrary.import definition (the v2 shell).
func (s *Server) olImportDef() opsregistry.OperationDef {
	return opsregistry.OperationDef{
		ID:              olImportOpID,
		Liveness:        opsregistry.LivenessManual,
		Plugin:          "openlibrary",
		DisplayName:     "OpenLibrary Dump Import",
		Description:     "Import OpenLibrary data dump files into the local search store.",
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         12 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  olImportOpID,
		Permissions:     []auth.Permission{auth.PermSettingsManage},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite, opsregistry.CapFilesRead},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			var p olImportOpParams
			if len(rawParams) > 0 {
				if err := json.Unmarshal(rawParams, &p); err != nil {
					return fmt.Errorf("openlibrary.import: decode params: %w", err)
				}
			}
			env := olImportEnv{t7Env: t7HeadEnv(reporter, true)}
			if s.olService != nil {
				env.Writer = headOLImportWriter{svc: s.olService}
			}
			return olImportCore(ctx, p, env)
		},
	}
}

// RegisterOLImportOp registers the "openlibrary.import" v2 OperationDef.
func (s *Server) RegisterOLImportOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(s.olImportDef())
}

// olDownloadNative is the native books.openlibrary.download kind. HEAD runs it
// only live (no preview, a files-write capability), so a Preview run is refused.
// It writes dump files on disk.
func (s *Server) olDownloadNative(deps t7Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	kind := t7KindFor(olDownloadOpID)
	c := t7Common(s.olDownloadDef(), sched)
	c.Effects = t7Writes(ops.ResFiles)
	return tasks.Task(kind, tasks.TaskSpec[json.RawMessage, struct{}]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (struct{}, error) {
			if err := t7LiveOnly(rc, kind); err != nil {
				return struct{}{}, err
			}
			p, err := t7DecodeNative[olDownloadOpParams](kind, raw)
			if err != nil {
				return struct{}{}, err
			}
			w, err := t7NewWriter(kind, deps, rc)
			if err != nil {
				return struct{}{}, err
			}
			return struct{}{}, olDownloadCore(p, olDownloadEnv{t7Env: t7NativeEnv(rc), Writer: w})
		},
	})
}

// olImportNative is the native books.openlibrary.import kind. HEAD runs it only
// live (no preview, a library-write capability), so a Preview run is refused.
// It writes the local OpenLibrary search store under the target directory.
func (s *Server) olImportNative(deps t7Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	kind := t7KindFor(olImportOpID)
	c := t7Common(s.olImportDef(), sched)
	c.Effects = t7Writes(ops.ResFiles)
	return tasks.Task(kind, tasks.TaskSpec[json.RawMessage, struct{}]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (struct{}, error) {
			if err := t7LiveOnly(rc, kind); err != nil {
				return struct{}{}, err
			}
			p, err := t7DecodeNative[olImportOpParams](kind, raw)
			if err != nil {
				return struct{}{}, err
			}
			w, err := t7NewWriter(kind, deps, rc)
			if err != nil {
				return struct{}{}, err
			}
			return struct{}{}, olImportCore(rc.Context(), p, olImportEnv{t7Env: t7NativeEnv(rc), Writer: w})
		},
	})
}

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterOLDownloadOp(reg) })
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterOLImportOp(reg) })
}
