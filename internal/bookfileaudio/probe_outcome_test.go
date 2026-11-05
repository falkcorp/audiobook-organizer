// file: internal/bookfileaudio/probe_outcome_test.go
// version: 1.0.0
// guid: ed5ca626-a270-4fc6-a767-04807741441d
// last-edited: 2026-11-04

package bookfileaudio

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/audioutil"
	"github.com/falkcorp/audiobook-organizer/internal/boundedcall"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/mediainfo"
	"github.com/falkcorp/audiobook-organizer/internal/testutil/durtest"
)

// recLog is a LevelLogger that keeps what it was asked to log.
type recLog struct {
	mu    sync.Mutex
	warns []string
	debug []string
}

func (l *recLog) add(dst *[]string, msg string, args []any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	*dst = append(*dst, fmt.Sprintf(msg, args...))
}
func (l *recLog) Trace(string, ...any)     {}
func (l *recLog) Info(string, ...any)      {}
func (l *recLog) Error(string, ...any)     {}
func (l *recLog) Debug(m string, a ...any) { l.add(&l.debug, m, a) }
func (l *recLog) Warn(m string, a ...any)  { l.add(&l.warns, m, a) }
func (l *recLog) warnCount() (n int)       { l.mu.Lock(); defer l.mu.Unlock(); return len(l.warns) }

func resetWarned(t *testing.T) {
	t.Helper()
	unavailableWarned.Clear()
	t.Cleanup(unavailableWarned.Clear)
}

func TestClassifyProbeError(t *testing.T) {
	exitErr := func() error {
		err := exec.Command("sh", "-c", "exit 1").Run()
		if err == nil {
			t.Fatal("expected an exit error")
		}
		return err
	}()
	cases := []struct {
		name        string
		err         error
		wantKind    FailureKind
		wantPrefix  string // LastError prefix when it counts
		wantCounted bool
	}{
		{"ffprobe not installed", fmt.Errorf("ffprobe /x: %w", exec.ErrNotFound), FailureEnvironment, "", false},
		{"ffprobe sentinel", audioutil.ErrFFprobeNotAvailable, FailureEnvironment, "", false},
		{"cancelled run", fmt.Errorf("probe: %w", context.Canceled), FailureEnvironment, "", false},
		{"file absent", fmt.Errorf("failed to open file: %w", fs.ErrNotExist), FailureAbsent, "", false},
		{"absent AND exit error (stat added ErrNotExist)", fmt.Errorf("%w: %w", fs.ErrNotExist, exitErr), FailureAbsent, "", false},
		{"exit error", fmt.Errorf("ffprobe /x: %w", exitErr), FailureFile, "ffprobe /x: exit status 1", true},
		{"zero seconds", errors.New("probe returned 0s"), FailureFile, "probe returned 0s", true},
		{"unparseable output", errors.New("ffprobe duration parse /x: invalid syntax"), FailureFile, "ffprobe duration parse", true},
		{"bookfileaudio timeout", ErrProbeTimeout, FailureTimeout, database.DurationProbeTimeoutPrefix, true},
		{"bounded call timeout", fmt.Errorf("extract: %w", boundedcall.ErrTimeout), FailureTimeout, database.DurationProbeTimeoutPrefix, true},
		{"ffprobe context deadline", fmt.Errorf("ffprobe /x: %w: %w", context.DeadlineExceeded, exitErr), FailureTimeout, database.DurationProbeTimeoutPrefix, true},
		{"nil", nil, FailureFile, "probe failed", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, msg := ClassifyProbeError(tc.err)
			if kind != tc.wantKind {
				t.Fatalf("kind = %v, want %v (msg %q)", kind, tc.wantKind, msg)
			}
			if kind.CountsAsAttempt() != tc.wantCounted {
				t.Fatalf("CountsAsAttempt = %v, want %v", kind.CountsAsAttempt(), tc.wantCounted)
			}
			if tc.wantCounted && !strings.HasPrefix(msg, tc.wantPrefix) {
				t.Fatalf("LastError %q, want prefix %q", msg, tc.wantPrefix)
			}
			if kind == FailureTimeout && !database.IsDurationTimeoutError(msg) {
				t.Fatalf("a timeout LastError %q must satisfy IsDurationTimeoutError", msg)
			}
		})
	}
}

func TestClassifyProbeError_LastErrorIsOneBoundedLine(t *testing.T) {
	_, msg := ClassifyProbeError(errors.New("line one\nline two\t" + strings.Repeat("x", 1000)))
	if strings.ContainsAny(msg, "\n\t") {
		t.Fatalf("LastError must be a single line, got %q", msg)
	}
	if n := len([]rune(msg)); n > lastErrorMaxRunes+1 {
		t.Fatalf("LastError is %d runes, want <= %d", n, lastErrorMaxRunes+1)
	}
}

// TestEnsureDuration_ProbeOutcomes is the classification table end to end: the
// probe is injected, EnsureDuration classifies it, and the row says what
// happened.
func TestEnsureDuration_ProbeOutcomes(t *testing.T) {
	exitErr := exec.Command("sh", "-c", "exit 1").Run()
	cases := []struct {
		name         string
		probe        func(string) (*mediainfo.MediaInfo, error)
		wantSource   Source
		wantState    database.BookFileDurationState
		wantAttempts int
		wantDur      int
		wantErrHas   string
	}{
		{
			name: "measured",
			probe: func(string) (*mediainfo.MediaInfo, error) {
				return &mediainfo.MediaInfo{Duration: 27000, Codec: "AAC"}, nil
			},
			wantSource: SourceProbe, wantState: database.DurationKnown, wantDur: 27000,
		},
		{
			name: "binary missing (Extract error): no attempt",
			probe: func(string) (*mediainfo.MediaInfo, error) {
				return nil, fmt.Errorf("ffprobe: %w", exec.ErrNotFound)
			},
			wantSource: SourceProbeUnavailable, wantState: database.DurationUnprobed,
		},
		{
			name: "binary missing (estimate carries the ffprobe error): no attempt",
			probe: func(string) (*mediainfo.MediaInfo, error) {
				return &mediainfo.MediaInfo{Duration: 500, DurationEstimated: true, DurationErr: fmt.Errorf("ffprobe: %w", exec.ErrNotFound)}, nil
			},
			wantSource: SourceProbeUnavailable, wantState: database.DurationUnprobed,
		},
		{
			name: "file absent: no attempt",
			probe: func(string) (*mediainfo.MediaInfo, error) {
				return nil, fmt.Errorf("failed to open file: %w", fs.ErrNotExist)
			},
			wantSource: SourceProbeUnavailable, wantState: database.DurationUnprobed,
		},
		{
			name:       "exit error: an attempt",
			probe:      func(string) (*mediainfo.MediaInfo, error) { return nil, fmt.Errorf("ffprobe: %w", exitErr) },
			wantSource: SourceProbeFailed, wantState: database.DurationProbeFailed, wantAttempts: 1, wantErrHas: "exit status 1",
		},
		{
			name: "estimate with an exit error: an attempt carrying the ffprobe error, never known",
			probe: func(string) (*mediainfo.MediaInfo, error) {
				return &mediainfo.MediaInfo{Duration: 500, DurationEstimated: true, DurationErr: fmt.Errorf("ffprobe: %w", exitErr)}, nil
			},
			wantSource: SourceProbeFailed, wantState: database.DurationProbeFailed, wantAttempts: 1, wantErrHas: "exit status 1",
		},
		{
			name: "estimate with no underlying error: an attempt that says it was an estimate",
			probe: func(string) (*mediainfo.MediaInfo, error) {
				return &mediainfo.MediaInfo{Duration: 500, DurationEstimated: true}, nil
			},
			wantSource: SourceProbeFailed, wantState: database.DurationProbeFailed, wantAttempts: 1, wantErrHas: "estimated",
		},
		{
			name:       "0 s with no error: an attempt",
			probe:      func(string) (*mediainfo.MediaInfo, error) { return &mediainfo.MediaInfo{Codec: "AAC"}, nil },
			wantSource: SourceProbeFailed, wantState: database.DurationProbeFailed, wantAttempts: 1, wantErrHas: "probe returned 0s",
		},
		{
			name: "0 s reported by ffprobe: an attempt",
			probe: func(string) (*mediainfo.MediaInfo, error) {
				return &mediainfo.MediaInfo{DurationErr: errors.New("probe returned 0s")}, nil
			},
			wantSource: SourceProbeFailed, wantState: database.DurationProbeFailed, wantAttempts: 1, wantErrHas: "probe returned 0s",
		},
		{
			name:       "no media info and no error: an attempt",
			probe:      func(string) (*mediainfo.MediaInfo, error) { return nil, nil },
			wantSource: SourceProbeFailed, wantState: database.DurationProbeFailed, wantAttempts: 1, wantErrHas: "no media info",
		},
		{
			name: "ffprobe deadline: an attempt marked as a timeout",
			probe: func(string) (*mediainfo.MediaInfo, error) {
				return &mediainfo.MediaInfo{DurationErr: fmt.Errorf("ffprobe: %w: %w", context.DeadlineExceeded, exitErr)}, nil
			},
			wantSource: SourceProbeFailed, wantState: database.DurationProbeFailed, wantAttempts: 1, wantErrHas: database.DurationProbeTimeoutPrefix,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetWarned(t)
			t.Cleanup(SetProbeForTesting(tc.probe))
			row := database.BookFile{FilePath: "/lib/Year One/01.m4b"}
			got := EnsureDuration(&row, Known{}, &recLog{})
			if got != tc.wantSource {
				t.Errorf("source = %v, want %v", got, tc.wantSource)
			}
			if row.DurationState() != tc.wantState {
				t.Errorf("state = %v, want %v", row.DurationState(), tc.wantState)
			}
			if durtest.Sec(row.Duration) != tc.wantDur {
				t.Errorf("Duration = %d, want %d", durtest.Sec(row.Duration), tc.wantDur)
			}
			ok := row.Scan.DurationOK
			if ok.Attempts != tc.wantAttempts {
				t.Errorf("Attempts = %d, want %d", ok.Attempts, tc.wantAttempts)
			}
			if tc.wantState == database.DurationKnown {
				if !ok.Done || ok.LastError != "" {
					t.Errorf("a measured row must be {Done}, got %+v", ok)
				}
			} else if ok.Done {
				t.Errorf("a row with no duration must not be Done: %+v", ok)
			}
			if !strings.Contains(ok.LastError, tc.wantErrHas) {
				t.Errorf("LastError %q, want it to contain %q", ok.LastError, tc.wantErrHas)
			}
			if tc.wantAttempts == 0 && ok != (database.StageStatus{Done: tc.wantState == database.DurationKnown}) {
				t.Errorf("an uncounted outcome must write nothing: %+v", ok)
			}
		})
	}
}

// The same classification through Known.Info, the path the scanner takes with
// the info it already read.
func TestEnsureDuration_KnownInfoOutcomes(t *testing.T) {
	t.Cleanup(SetProbeForTesting(func(string) (*mediainfo.MediaInfo, error) {
		t.Error("Known.Info is final: the file must not be probed again")
		return nil, nil
	}))
	resetWarned(t)

	missing := database.BookFile{FilePath: "/lib/Year One/01.m4b"}
	got := EnsureDuration(&missing, Known{Info: &mediainfo.MediaInfo{
		Codec: "AAC", Duration: 400, DurationEstimated: true, DurationErr: fmt.Errorf("ffprobe: %w", exec.ErrNotFound),
	}}, &recLog{})
	if got != SourceProbeUnavailable || missing.Scan.DurationOK.Attempts != 0 || missing.Codec != "AAC" {
		t.Fatalf("estimate behind a missing ffprobe: source %v, %+v, codec %q", got, missing.Scan.DurationOK, missing.Codec)
	}

	estimate := database.BookFile{FilePath: "/lib/Year One/02.m4b"}
	got = EnsureDuration(&estimate, Known{Info: &mediainfo.MediaInfo{
		Duration: 400, DurationEstimated: true, DurationErr: errors.New("ffprobe duration parse: N/A"),
	}}, &recLog{})
	if got != SourceProbeFailed || estimate.Duration != nil || estimate.Scan.DurationOK.Attempts != 1 {
		t.Fatalf("estimate after a parse failure: source %v, dur %v, %+v", got, estimate.Duration, estimate.Scan.DurationOK)
	}
}

// Attempts accumulate on the row the caller holds and reach Unreadable at the
// ceiling; a later success replaces the record.
func TestEnsureDuration_AttemptsReachUnreadableThenSuccessClears(t *testing.T) {
	resetWarned(t)
	failing := true
	t.Cleanup(SetProbeForTesting(func(string) (*mediainfo.MediaInfo, error) {
		if failing {
			return &mediainfo.MediaInfo{DurationErr: errors.New("probe returned 0s")}, nil
		}
		return &mediainfo.MediaInfo{Duration: 5400}, nil
	}))
	row := database.BookFile{FilePath: "/lib/The Killing Joke/01.m4b"}
	for i := 1; i <= database.DeepScanMaxAttempts; i++ {
		EnsureDuration(&row, Known{}, &recLog{})
		want := database.DurationProbeFailed
		if i == database.DeepScanMaxAttempts {
			want = database.DurationUnreadable
		}
		if row.Scan.DurationOK.Attempts != i || row.DurationState() != want {
			t.Fatalf("after %d probes: %+v state %v, want state %v", i, row.Scan.DurationOK, row.DurationState(), want)
		}
	}
	failing = false
	if got := EnsureDuration(&row, Known{}, &recLog{}); got != SourceProbe {
		t.Fatalf("source = %v", got)
	}
	if row.DurationState() != database.DurationKnown || row.Scan.DurationOK != (database.StageStatus{Done: true}) {
		t.Fatalf("success must replace the failure record: %+v", row.Scan.DurationOK)
	}
}

func TestEnsureDuration_WarnsOncePerUnavailableCause(t *testing.T) {
	resetWarned(t)
	t.Cleanup(SetProbeForTesting(func(string) (*mediainfo.MediaInfo, error) {
		return nil, fmt.Errorf("ffprobe: %w", exec.ErrNotFound)
	}))
	log := &recLog{}
	for i := 0; i < 5; i++ {
		row := database.BookFile{FilePath: fmt.Sprintf("/lib/Year One/%02d.m4b", i)}
		if got := EnsureDuration(&row, Known{}, log); got != SourceProbeUnavailable {
			t.Fatalf("source = %v", got)
		}
	}
	if log.warnCount() != 1 {
		t.Fatalf("warned %d times for one cause, want 1: %v", log.warnCount(), log.warns)
	}
	// A different cause warns on its own.
	t.Cleanup(SetProbeForTesting(func(string) (*mediainfo.MediaInfo, error) {
		return nil, fmt.Errorf("open: %w", fs.ErrNotExist)
	}))
	row := database.BookFile{FilePath: "/lib/gone.m4b"}
	EnsureDuration(&row, Known{}, log)
	if log.warnCount() != 2 {
		t.Fatalf("a new cause must warn once: %d %v", log.warnCount(), log.warns)
	}
}

func TestProbeError(t *testing.T) {
	boom := errors.New("boom")
	if ProbeError(&mediainfo.MediaInfo{Duration: 10}, nil) != nil {
		t.Error("a real duration is not an error")
	}
	if ProbeError(&mediainfo.MediaInfo{Duration: 10}, boom) != boom {
		t.Error("an explicit error wins")
	}
	if ProbeError(nil, nil) == nil || ProbeError(&mediainfo.MediaInfo{}, nil) == nil {
		t.Error("no media info / 0 s must be an error")
	}
	under := errors.New("under")
	if got := ProbeError(&mediainfo.MediaInfo{Duration: 9, DurationEstimated: true, DurationErr: under}, nil); !errors.Is(got, under) {
		t.Errorf("an estimate must carry the underlying error, got %v", got)
	}
}

func TestRecordProbeFailure_NilArgs(t *testing.T) {
	if RecordProbeFailure(nil, "/x", errors.New("e"), nil) != SourceNone {
		t.Error("nil row")
	}
	row := database.BookFile{}
	if RecordProbeFailure(&row, "/x", nil, nil) != SourceNone || row.Scan.DurationOK.Attempts != 0 {
		t.Error("nil error must write nothing")
	}
}
