// file: internal/fingerprint/workerapi/types.go
// version: 1.1.0
// guid: d5f6ff4f-9158-43e6-b27a-0ef43f926bfb
// last-edited: 2026-09-19

// Package workerapi is the wire format of the remote fingerprint worker API
// (/api/v1/fingerprint/worker/*), shared by the server (the lease manager in
// internal/plugins/acoustid) and the fp-worker client. Design:
// .claude/notes/windowed-fingerprint-design-2026-09-12.md sections (d), (d2).
//
// Paths never cross the wire as absolute strings: a job names a root
// ("libroot") and the raw bytes of a path relative to it (RelB64). The worker
// joins those bytes onto its own mount of the root; results carry only the
// job ID and the opaque Ref, never a worker-side path.
package workerapi

import (
	"errors"
	"time"
)

// Errors the server's lease manager returns; the HTTP layer maps each to a
// status code.
var (
	// ErrNoRun: no live acoustid.window-backfill is running (503).
	ErrNoRun = errors.New("fingerprint worker: acoustid.window-backfill is not running")
	// ErrLeaseGone: the lease expired, was reclaimed, or never existed (410).
	ErrLeaseGone = errors.New("fingerprint worker: lease expired or reclaimed")
	// ErrToolsNotAllowed: pipeline or tool versions not allowlisted (409).
	ErrToolsNotAllowed = errors.New("fingerprint worker: pipeline or tool versions not allowlisted")
	// ErrTooManyLeases: the worker already holds MaxLeasesPerWorker (429).
	ErrTooManyLeases = errors.New("fingerprint worker: too many open leases for this worker")
	// ErrBadRequest: malformed request (400).
	ErrBadRequest = errors.New("fingerprint worker: bad request")
)

// Limits of the protocol (design (d), "Lease sizing").
const (
	// MaxJobsPerLease caps one lease, whatever the worker asks for.
	MaxJobsPerLease = 50
	// LeaseTTL is how long a lease lives without a renewal.
	LeaseTTL = 10 * time.Minute
	// RenewEvery is how often a worker should renew a lease it still holds.
	RenewEvery = 2 * time.Minute
	// SweepEvery is how often the server reclaims expired leases.
	SweepEvery = 30 * time.Second
	// MaxLeasesPerWorker caps concurrent leases held by one worker ID.
	MaxLeasesPerWorker = 2
	// MaxBodyBytes caps every request body.
	MaxBodyBytes = 8 << 20
	// MinFrames is the fewest frames an accepted window may carry.
	MinFrames = 80
	// DecodedTolerance is how far decoded_sec may stray from length_sec.
	DecodedTolerance = 0.05
)

// Result statuses the server answers for each posted job.
const (
	StatusAccepted  = "accepted"
	StatusDuplicate = "duplicate"
	StatusStale     = "stale"
	StatusRejected  = "rejected"
)

// Outcomes a worker reports for a job.
const (
	OutcomeOK          = "ok"
	OutcomeNotFound    = "not_found"
	OutcomeDecodeError = "decode_error"
	OutcomeTimeout     = "timeout"
	// OutcomeStale: the file's size or mtime on the worker's mount did not
	// match the job's.
	OutcomeStale = "stale"
	// OutcomeRejected: the worker refused the job before opening anything
	// (a path check failed, or the root is not configured).
	OutcomeRejected = "rejected"
)

// Root describes one named root in the hello root map.
type Root struct {
	ID string `json:"id"`
	// Remote reports whether jobs under this root are offered to workers. In
	// v1 only "libroot" is.
	Remote bool `json:"remote"`
}

// ToolVersions is one allowlisted (fpcalc, ffmpeg) build pair.
type ToolVersions struct {
	Fpcalc string `json:"fpcalc_version"`
	FFmpeg string `json:"ffmpeg_version"`
}

// CalibrationFile is a file the server has already fingerprinted, for the
// worker's startup gate: it proves the worker's root mapping points at the
// same tree (size and the SHA-256 of the first 64 KiB) and that its pipeline
// is byte-identical (the window specs and the SHA-256 of each stored raw
// print).
type CalibrationFile struct {
	Root      string              `json:"root"`
	RelB64    string              `json:"rel_b64"`
	Rel       string              `json:"rel"` // display only; may be lossy
	Size      int64               `json:"size"`
	MtimeUnix int64               `json:"mtime_unix"`
	Head64K   string              `json:"head_64k_sha256"`
	Windows   []CalibrationWindow `json:"windows"`
}

// CalibrationWindow is one stored window of a calibration file. Only windows
// the server cut itself are offered, never a worker's: the tool pair says
// which server build made the print the worker must reproduce.
type CalibrationWindow struct {
	Window
	RawSHA256 string `json:"raw_sha256"`
	Frames    int    `json:"frames"`
	ToolVersions
}

// HelloResponse answers GET hello.
type HelloResponse struct {
	Roots        []Root            `json:"roots"`
	Pipeline     string            `json:"pipeline"`
	WindowSet    string            `json:"window_set"`
	ToolVersions []ToolVersions    `json:"tool_versions"`
	Calibration  []CalibrationFile `json:"calibration"`
	Limits       Limits            `json:"limits"`
}

// Limits restates the protocol constants so a worker need not hard-code them.
type Limits struct {
	MaxJobsPerLease    int `json:"max_jobs_per_lease"`
	LeaseTTLSec        int `json:"lease_ttl_sec"`
	RenewEverySec      int `json:"renew_every_sec"`
	MaxLeasesPerWorker int `json:"max_leases_per_worker"`
	MaxBodyBytes       int `json:"max_body_bytes"`
}

// LeaseRequest is the body of POST lease.
type LeaseRequest struct {
	WorkerID      string `json:"worker_id"`
	MaxJobs       int    `json:"max_jobs"`
	FpcalcVersion string `json:"fpcalc_version"`
	FFmpegVersion string `json:"ffmpeg_version"`
	Pipeline      string `json:"pipeline"`
}

// Window is one window to cut: the fields of fingerprint.WindowSpec.
type Window struct {
	Kind        string  `json:"kind"`
	SlotBP      int     `json:"slot_bp"`
	OffsetSec   float64 `json:"offset_sec"`
	LengthSec   float64 `json:"length_sec"`
	CoversWhole bool    `json:"covers_whole,omitempty"`
}

// Job is one file to fingerprint.
type Job struct {
	JobID          string   `json:"job_id"`
	Ref            string   `json:"ref"`
	Root           string   `json:"root"`
	RelB64         string   `json:"rel_b64"`
	Rel            string   `json:"rel"` // display only; may be lossy
	Size           int64    `json:"size"`
	MtimeUnix      int64    `json:"mtime_unix"`
	DurationSec    float64  `json:"duration_used_sec"`
	DurationSource string   `json:"duration_source"`
	WindowSet      string   `json:"window_set"`
	Windows        []Window `json:"windows"`
}

// LeaseResponse answers POST lease (200; 204 carries no body).
type LeaseResponse struct {
	LeaseID   string    `json:"lease_id"`
	ExpiresAt time.Time `json:"expires_at"`
	Jobs      []Job     `json:"jobs"`
}

// RenewRequest is the body of POST lease/{id}/renew. Only the worker that
// holds the lease may renew it.
type RenewRequest struct {
	WorkerID string `json:"worker_id"`
}

// RenewResponse answers POST lease/{id}/renew.
type RenewResponse struct {
	LeaseID   string    `json:"lease_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

// ReleaseRequest is the body of POST lease/{id}/release. Empty JobIDs hands
// back every unfinished job of the lease.
type ReleaseRequest struct {
	WorkerID string   `json:"worker_id"`
	JobIDs   []string `json:"job_ids,omitempty"`
}

// ReleaseResponse answers POST lease/{id}/release.
type ReleaseResponse struct {
	Requeued int `json:"requeued"`
}

// WindowResult is one computed window.
type WindowResult struct {
	Window
	DecodedSec float64 `json:"decoded_sec"`
	Frames     int     `json:"frames"`
	Algorithm  int     `json:"algorithm"`
	// RawB64 is the raw chromaprint (fpcalc -raw), little-endian uint32 per
	// frame, base64-encoded.
	RawB64 string `json:"raw_b64"`
}

// JobResult is one job's outcome.
type JobResult struct {
	JobID         string         `json:"job_id"`
	Ref           string         `json:"ref"`
	Outcome       string         `json:"outcome"`
	Error         string         `json:"error,omitempty"`
	Pipeline      string         `json:"pipeline,omitempty"`
	FpcalcVersion string         `json:"fpcalc_version,omitempty"`
	FFmpegVersion string         `json:"ffmpeg_version,omitempty"`
	Windows       []WindowResult `json:"windows,omitempty"`
}

// ResultsRequest is the body of POST results.
type ResultsRequest struct {
	WorkerID string      `json:"worker_id"`
	LeaseID  string      `json:"lease_id"`
	Results  []JobResult `json:"results"`
}

// JobStatus is the server's verdict on one posted job.
type JobStatus struct {
	JobID  string `json:"job_id"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// ResultsResponse answers POST results.
type ResultsResponse struct {
	Statuses []JobStatus `json:"statuses"`
}
