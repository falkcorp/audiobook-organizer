// file: core/outbox/fault_test.go
// version: 1.0.1
// guid: 7b2e4c0d-91a3-4f56-8d17-6c0a5e3f2b94
// last-edited: 2026-11-12

package outbox_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/internal/outboxstore"
	"github.com/falkcorp/mediacore/outbox"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/codec"
	"github.com/falkcorp/mediacore/tasks"
)

// A failure of the store is told apart by what an operator can do about it,
// through the sentinels the standard library and storage expose and never
// through the error's text; anything else is "error".
func TestOutbox_FaultKindIsAClosedTokenPerCause(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"plain", errors.New("the Joker is loose"), "error"},
		{"disk full", fmt.Errorf("write wal: %w", syscall.ENOSPC), "no_space"},
		{"disk full under a path error", &fs.PathError{Op: "write", Path: "wal", Err: syscall.ENOSPC}, "no_space"},
		{"quota", syscall.EDQUOT, "no_space"},
		{"io", &fs.PathError{Op: "read", Path: "sst", Err: syscall.EIO}, "io"},
		{"permission", &fs.PathError{Op: "open", Path: "sst", Err: syscall.EACCES}, "permission"},
		{"permission sentinel", fmt.Errorf("open: %w", fs.ErrPermission), "permission"},
		{"closed store", fmt.Errorf("get: %w", storage.ErrClosed), "closed"},
		{"closed file", os.ErrClosed, "closed"},
		{"read only", storage.ErrReadOnly, "read_only"},
		{"too large", storage.ErrBatchTooLarge, "too_large"},
		{"conflict", storage.ErrConflict, "conflict"},
		{"fenced", tasks.ErrFenced, "fenced"},
		{"canceled", context.Canceled, "canceled"},
		{"deadline", fmt.Errorf("commit: %w", context.DeadlineExceeded), "timeout"},
		{"unknown version", codec.ErrUnknownVersion, "unknown_version"},
		{"corrupt row", outboxstore.ErrCorruptRow, "corrupt_row"},
		{"partitions changed", outboxstore.ErrPartitionsChanged, "partitions_changed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := outbox.FaultKind(tc.err); got != tc.want {
				t.Fatalf("FaultKind(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

// A consumer whose cursors cannot be created is left alone for a while. The
// creation is a write of the run (an intent row and a renewed hold, whatever the
// function inside returns), so retrying a persistent failure on every loop would
// write on every loop. Here the topic's seq row is unreadable, a failure that
// is not a partition change. The failure is still reported, once.
func TestOutbox_FailingCursorInitIsNotRetriedEveryLoop(t *testing.T) {
	logs := &logCapture{}
	r := newRig(t, rigOpts{log: logs.logger(), noRelay: true, register: consumeInto(&recorder{}, consumerN)})
	r.putRaw(outboxstore.SeqKey(noticeName), []byte(`zNEWER-BUILD-ENCODING`))
	r.startRelay()
	eventually(t, "the failure to be reported", func() bool { return logs.count("cursors of "+consumerN+" failed") == 1 })
	time.Sleep(100 * time.Millisecond)
	before := r.intentRows()
	const wakes = 200
	for range wakes {
		r.ob.Wake()
		time.Sleep(3 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	if grown := r.intentRows() - before; grown*10 > wakes {
		t.Fatalf("%d ledger rows written over %d wakes for a consumer whose cursors cannot be made, want at most a tenth as many", grown, wakes)
	}
	if n := logs.count("cursors of " + consumerN + " failed"); n != 1 {
		t.Fatalf("the failure was logged %d times in a minute, want once:\n%s", n, logs.text())
	}
	if r.cursors(consumerN) != 0 {
		t.Fatalf("cursors were made over an unreadable seq row")
	}
}

// RefreshStatus draws on the same once-a-minute allowance as the relay's own
// warnings: an operator action that refreshes the status in a loop, or the
// relay's 15 second tick, cannot turn a persistent failure into a stream.
func TestOutbox_StatusFailureWarningIsRateLimited(t *testing.T) {
	logs := &logCapture{}
	r := newRig(t, rigOpts{log: logs.logger(), register: consumeInto(&recorder{}, consumerN)})
	// A key under cur/ that is no pair's: a failure of the store's layout, not a
	// row that a status read steps over.
	bad := append(append([]byte(nil), outboxstore.CurRoot()...), 'j')
	r.putRaw(bad, []byte(`x`))
	ctx := context.Background()
	if _, err := r.ob.Status(ctx); err == nil {
		t.Fatal("the planted key did not make Status fail; this test would prove nothing")
	}
	for range 50 {
		r.ob.RefreshStatus(ctx)
	}
	if n := logs.count("outbox: status failed"); n != 1 {
		t.Fatalf("%d status warnings for 50 failed refreshes, want 1:\n%s", n, logs.text())
	}
	for line := range strings.SplitSeq(logs.text(), "\n") {
		if strings.Contains(line, "status failed") && !strings.Contains(line, "fault=error") {
			t.Fatalf("the warning does not carry the fault token: %s", line)
		}
	}
}
