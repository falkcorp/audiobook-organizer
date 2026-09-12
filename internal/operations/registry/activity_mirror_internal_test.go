// file: internal/operations/registry/activity_mirror_internal_test.go
// version: 1.0.0
// guid: 76ecb51d-8b9c-49ca-b780-26e0b247b4cc
// last-edited: 2026-09-11

package registry

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// gatedRecorder blocks every Record until release is closed, standing in for
// the SQLite activity writer while ANALYZE holds its only connection.
type gatedRecorder struct {
	release chan struct{}
	mu      sync.Mutex
	got     []string
}

func newGatedRecorder(open bool) *gatedRecorder {
	g := &gatedRecorder{release: make(chan struct{})}
	if open {
		close(g.release)
	}
	return g
}

func (g *gatedRecorder) Record(e database.ActivityEntry) error {
	<-g.release
	g.mu.Lock()
	g.got = append(g.got, e.Summary)
	g.mu.Unlock()
	return nil
}

func (g *gatedRecorder) delivered() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.got...)
}

// The 2026-09-11 incident in miniature: the store is wedged, and the op keeps
// logging. Before the mirror, the first Record call never returned.
func TestActivityMirror_RecordNeverBlocksOnAWedgedStore(t *testing.T) {
	inner := newGatedRecorder(false)
	m := newActivityMirror(inner, nil, 4)

	const n = 100
	returned := make(chan struct{})
	go func() {
		for i := range n {
			_ = m.Record(database.ActivityEntry{Summary: fmt.Sprint(i)})
		}
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("Record blocked behind a wedged store")
	}

	// One entry may be held by the drain goroutine, blocked in inner.Record,
	// plus up to queue-size entries buffered; everything else was dropped.
	assert.GreaterOrEqual(t, m.Dropped(), uint64(n-4-1))

	close(inner.release)
	m.Stop()
	assert.Equal(t, n, len(inner.delivered())+int(m.Dropped()),
		"every entry is either delivered or counted as dropped")
}

func TestActivityMirror_DeliversInOrderAndDrainsOnStop(t *testing.T) {
	inner := newGatedRecorder(true)
	m := newActivityMirror(inner, nil, 64)

	want := make([]string, 50)
	for i := range want {
		want[i] = fmt.Sprint(i)
		require.NoError(t, m.Record(database.ActivityEntry{Summary: want[i]}))
	}
	m.Stop()

	assert.Equal(t, want, inner.delivered())
	assert.Zero(t, m.Dropped())
}

func TestActivityMirror_RecordAfterStopDropsWithoutPanicking(t *testing.T) {
	inner := newGatedRecorder(true)
	m := newActivityMirror(inner, nil, 4)
	m.Stop()
	m.Stop() // idempotent

	require.NotPanics(t, func() { _ = m.Record(database.ActivityEntry{Summary: "late"}) })
	assert.Empty(t, inner.delivered(), "nothing reaches the store after Stop")
	assert.Equal(t, uint64(1), m.Dropped())
}

func TestSetActivityRecorder_WrapsAndReplaces(t *testing.T) {
	r := newTestRegistryWithStore(nil)

	first := newGatedRecorder(true)
	r.SetActivityRecorder(first)
	m1, ok := r.activityRecorder.(*activityMirror)
	require.True(t, ok, "the recorder the reporters see must be the non-blocking mirror")

	r.SetActivityRecorder(newGatedRecorder(true))
	select {
	case <-m1.done:
	default:
		t.Fatal("replacing the recorder must stop the previous mirror's goroutine")
	}

	// nil must clear to an untyped nil so recordActivity's nil check still
	// short-circuits; a typed-nil *activityMirror would pass it and panic.
	r.SetActivityRecorder(nil)
	assert.Nil(t, r.activityRecorder)
	assert.Nil(t, r.activityMirror)
}
