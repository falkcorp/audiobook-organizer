// file: internal/server/flight_recorder.go
// version: 1.0.1
// guid: c64f779e-5ec5-46b3-8883-8d6f1b56bec9
// last-edited: 2026-10-19

package server

import (
	"path/filepath"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	diagflight "github.com/falkcorp/audiobook-organizer/internal/diag/flight"
)

// traceSnapshotter is the one thing the two watchdogs need from the runtime
// flight recorder: ask for a snapshot and return at once. *diagflight.Recorder
// satisfies it; tests substitute a recorder of the requests.
type traceSnapshotter interface {
	Snapshot(reason string)
}

// newFlightRecorder builds the server's flight recorder (not started; see
// startFlightRecorder). Traces go to a "traces" directory beside the database,
// the same sibling-directory rule the HNSW index uses. With no DatabasePath (most tests) the recorder is disabled.
func newFlightRecorder() *diagflight.Recorder {
	var dir string
	if config.AppConfig.DatabasePath != "" {
		dir = filepath.Join(filepath.Dir(config.AppConfig.DatabasePath), "traces")
	}
	return diagflight.New(diagflight.Config{Dir: dir})
}

// startFlightRecorder starts recording. Failure is logged and ignored: tracing
// is evidence for an incident, never a reason to refuse to serve.
func (s *Server) startFlightRecorder() {
	if err := s.flight.Start(); err != nil {
		lifecycleLog.Warn("runtime flight recorder not started; watchdog snapshots are off: %v", err)
	}
}

// requestTrace asks the flight recorder for a snapshot tagged reason. Never
// blocks; nil-safe.
func (s *Server) requestTrace(reason string) {
	if s.traceSnap != nil {
		s.traceSnap.Snapshot(reason)
	}
}

// requestWatchdogTrace is the ops registry's watchdog strike hook. kind is
// "stuck" or "never_reported"; reasons use '-' only, so the file is named
// ...-watchdog-stuck.trace or ...-watchdog-never-reported.trace.
func (s *Server) requestWatchdogTrace(kind string) {
	s.requestTrace("watchdog-" + strings.ReplaceAll(kind, "_", "-"))
}
