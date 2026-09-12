// file: internal/server/file_op_recovery_test.go
// version: 1.0.0
// guid: 2b8e5d17-4c6a-49f3-a0e1-7d93c5b28f46
// last-edited: 2026-09-12

package server

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeApplyRecoverer counts tag writes the way FinishApplyFileWork performs
// them: at most one per call (its write-once guard is pinned by metafetch's
// TestFinishApplyFileWork_WritesTagsOnce).
type fakeApplyRecoverer struct {
	calls     []string
	tagWrites int
}

func (f *fakeApplyRecoverer) FinishApplyFileWork(id, cover string, fileIO, writeTags bool) error {
	f.calls = append(f.calls, fmt.Sprintf("%s|%s|%v|%v", id, cover, fileIO, writeTags))
	if writeTags {
		f.tagWrites++
	}
	return nil
}

// The replay used to call ApplyMetadataFileIO and then WriteBackMetadataForBook,
// tagging every file twice under auto_write_tags_on_apply.
func TestRecoverApplyMetadataFileOp_WritesTagsOnce(t *testing.T) {
	f := &fakeApplyRecoverer{}
	var enqueued []string
	recoverApplyMetadataFileOp(f, func(id string) { enqueued = append(enqueued, id) }, "b1")

	assert.Equal(t, []string{"b1||true|true"}, f.calls, "one pass through the shared sequel")
	assert.Equal(t, 1, f.tagWrites)
	assert.Equal(t, []string{"b1"}, enqueued)

	recoverApplyMetadataFileOp(f, nil, "b2") // no batcher wired
	assert.Equal(t, 2, f.tagWrites)
}

// Auto-fetch's file work is queued on the pool under its own op type and runs
// holding the path lock.
func TestAutoFetchScheduler_RunsOnPoolUnderPathLock(t *testing.T) {
	pool := NewFileIOPool(1)
	defer pool.Stop()

	var mu sync.Mutex
	var events []string
	record := func(e string) { mu.Lock(); events = append(events, e); mu.Unlock() }
	lock := func(p string) func() { record("lock:" + p); return func() { record("unlock:" + p) } }

	release := make(chan struct{})
	sched := newAutoFetchScheduler(func() *FileIOPool { return pool }, lock)
	sched("b1", "/lib/a", func() { <-release; record("work") })

	var opTypes []string
	for _, j := range pool.PendingJobs() {
		opTypes = append(opTypes, j.OpType)
	}
	assert.Equal(t, []string{autoFetchFileOpType}, opTypes, "queued as auto-fetch work so a replay runs the auto-fetch rules")
	close(release)

	require.Eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(events) == 3 }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, []string{"lock:/lib/a", "work", "unlock:/lib/a"}, events)
}
