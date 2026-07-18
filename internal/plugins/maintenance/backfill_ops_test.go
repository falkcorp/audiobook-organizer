// file: internal/plugins/maintenance/backfill_ops_test.go
// version: 1.0.0
// guid: 7d8e9f0a-1b2c-3d4e-5f6a-7b8c9d0e1f2a
// last-edited: 2026-07-18

// Package maintenance tests for T09 (C2/H7): proves runExternalIDBackfill,
// runMalformedM4BRemux, and runMalformedM4BTranscode thread the op reporter's
// progress and error through to the underlying deps calls instead of
// swallowing them — the bug that let a multi-hour ffmpeg walk show a single
// "starting"/"complete" log line and never fail the op.
package maintenance

import (
	"context"
	"errors"
	"testing"
)

// t09OpsFakeDeps wraps fakeDeps (from title_backfill_test.go) and overrides
// the three C2/H7 methods with configurable error + progress-invocation
// behavior, so tests can assert the Run functions propagate both correctly.
// Named with a t09 prefix per RULE 9 (task-unique test helper names — a
// same-named helper in this package has broken main before).
type t09OpsFakeDeps struct {
	fakeDeps
	backfillErr  error
	remuxErr     error
	transcodeErr error
}

func (d t09OpsFakeDeps) BackfillExternalIDs(progress func(processed, total int, msg string)) error {
	if progress != nil {
		progress(1, 2, "half done")
	}
	return d.backfillErr
}

func (d t09OpsFakeDeps) RemuxMalformedM4BFiles(_ context.Context, progress func(processed, total int, msg string)) error {
	if progress != nil {
		progress(5, 10, "remuxing")
	}
	return d.remuxErr
}

func (d t09OpsFakeDeps) TranscodeMalformedM4BFiles(_ context.Context, progress func(processed, total int, msg string)) error {
	if progress != nil {
		progress(3, 7, "transcoding")
	}
	return d.transcodeErr
}

var _ ServerDeps = t09OpsFakeDeps{}

// t09ProgressReporter is a minimal fakeReporter wrapper that records every
// UpdateProgress call so tests can assert the callback threaded through.
type t09ProgressReporter struct {
	fakeReporter
	progressCalls []string
}

func (r *t09ProgressReporter) UpdateProgress(current, total int, msg string) error {
	r.progressCalls = append(r.progressCalls, msg)
	return r.fakeReporter.UpdateProgress(current, total, msg)
}

func TestRunExternalIDBackfill_ThreadsProgressAndSucceeds(t *testing.T) {
	p := New(t09OpsFakeDeps{})
	reporter := &t09ProgressReporter{}

	if err := p.runExternalIDBackfill(context.Background(), nil, reporter); err != nil {
		t.Fatalf("runExternalIDBackfill() unexpected error: %v", err)
	}
	if len(reporter.progressCalls) != 1 || reporter.progressCalls[0] != "half done" {
		t.Errorf("runExternalIDBackfill() progress calls = %v, want [\"half done\"]", reporter.progressCalls)
	}
}

// TestRunExternalIDBackfill_PropagatesError proves the H7 fix: a non-nil
// error from BackfillExternalIDs now fails the op instead of being demoted
// to a Warn log while the Run function unconditionally reports "complete".
func TestRunExternalIDBackfill_PropagatesError(t *testing.T) {
	wantErr := errors.New("backfill boom")
	p := New(t09OpsFakeDeps{backfillErr: wantErr})
	reporter := &t09ProgressReporter{}

	err := p.runExternalIDBackfill(context.Background(), nil, reporter)
	if !errors.Is(err, wantErr) {
		t.Fatalf("runExternalIDBackfill() error = %v, want %v", err, wantErr)
	}
}

func TestRunMalformedM4BRemux_ThreadsProgressAndSucceeds(t *testing.T) {
	p := New(t09OpsFakeDeps{})
	reporter := &t09ProgressReporter{}

	if err := p.runMalformedM4BRemux(context.Background(), nil, reporter); err != nil {
		t.Fatalf("runMalformedM4BRemux() unexpected error: %v", err)
	}
	if len(reporter.progressCalls) != 1 || reporter.progressCalls[0] != "remuxing" {
		t.Errorf("runMalformedM4BRemux() progress calls = %v, want [\"remuxing\"]", reporter.progressCalls)
	}
}

// TestRunMalformedM4BRemux_PropagatesError proves the C2 fix: a fatal setup
// failure from RemuxMalformedM4BFiles now fails the op.
func TestRunMalformedM4BRemux_PropagatesError(t *testing.T) {
	wantErr := errors.New("remux boom")
	p := New(t09OpsFakeDeps{remuxErr: wantErr})
	reporter := &t09ProgressReporter{}

	err := p.runMalformedM4BRemux(context.Background(), nil, reporter)
	if !errors.Is(err, wantErr) {
		t.Fatalf("runMalformedM4BRemux() error = %v, want %v", err, wantErr)
	}
}

func TestRunMalformedM4BTranscode_ThreadsProgressAndSucceeds(t *testing.T) {
	p := New(t09OpsFakeDeps{})
	reporter := &t09ProgressReporter{}

	if err := p.runMalformedM4BTranscode(context.Background(), nil, reporter); err != nil {
		t.Fatalf("runMalformedM4BTranscode() unexpected error: %v", err)
	}
	if len(reporter.progressCalls) != 1 || reporter.progressCalls[0] != "transcoding" {
		t.Errorf("runMalformedM4BTranscode() progress calls = %v, want [\"transcoding\"]", reporter.progressCalls)
	}
}

// TestRunMalformedM4BTranscode_PropagatesError proves the C2 fix: a fatal
// setup failure from TranscodeMalformedM4BFiles now fails the op.
func TestRunMalformedM4BTranscode_PropagatesError(t *testing.T) {
	wantErr := errors.New("transcode boom")
	p := New(t09OpsFakeDeps{transcodeErr: wantErr})
	reporter := &t09ProgressReporter{}

	err := p.runMalformedM4BTranscode(context.Background(), nil, reporter)
	if !errors.Is(err, wantErr) {
		t.Fatalf("runMalformedM4BTranscode() error = %v, want %v", err, wantErr)
	}
}
