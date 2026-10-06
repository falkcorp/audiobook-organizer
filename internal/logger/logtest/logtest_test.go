// file: internal/logger/logtest/logtest_test.go
// version: 1.0.0
// guid: 15376ffa-f78d-4179-86dc-c3e6131758b4
// last-edited: 2026-10-06

package logtest

import (
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// A goroutine still logging while the test reads is the case Buffer exists
// for; -race fails this test if the lock is removed.
func TestCapture_ConcurrentWritersAndReader(t *testing.T) {
	buf := Capture(t, slog.LevelWarn)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 50 {
				slog.Warn("background line")
				_ = buf.String()
			}
		})
	}
	wg.Wait()
	slog.Info("below level")
	if got := len(buf.Lines("background line")); got != 400 {
		t.Fatalf("captured %d lines, want 400", got)
	}
	if strings.Contains(buf.String(), "below level") {
		t.Fatal("a line below the capture level was kept")
	}
	buf.Reset()
	if buf.String() != "" {
		t.Fatal("Reset left content behind")
	}
}

func TestCapture_RestoresDefault(t *testing.T) {
	before := slog.Default()
	t.Run("capture", func(t *testing.T) {
		_ = Capture(t, slog.LevelDebug)
		if slog.Default() == before {
			t.Fatal("Capture did not install its logger")
		}
	})
	if slog.Default() != before {
		t.Fatal("slog.Default was not restored after the subtest")
	}
}

func TestCaptureRecords_KeepsLevelAndMessage(t *testing.T) {
	rec := CaptureRecords(t)
	slog.Warn("first", "k", "v")
	slog.Debug("second")
	got := rec.Records()
	if len(got) != 2 || got[0].Level != slog.LevelWarn || got[0].Message != "first" || got[1].Message != "second" {
		t.Fatalf("records = %+v", got)
	}
}

// Capture must refuse a parallel test: the swap is process-global.
func TestCapture_PanicsUnderParallel(t *testing.T) {
	t.Run("parallel", func(t *testing.T) {
		t.Parallel()
		defer func() {
			if recover() == nil {
				t.Error("Capture under t.Parallel did not panic")
			}
		}()
		_ = Capture(t, slog.LevelWarn)
	})
}
