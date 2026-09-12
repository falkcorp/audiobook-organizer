// file: internal/metafetch/version_group_siblings_test.go
// version: 1.0.0
// guid: 9ab09957-d8ac-43d1-adb7-067273050274
// last-edited: 2026-09-12

package metafetch

import (
	"bytes"
	"errors"
	"log/slog"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/stretchr/testify/require"
)

type lockedLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// A version-group read error is logged, not swallowed. Both callers
// (write-back to sibling copies, the library-copy lookup) keep going without
// the siblings, but before 2026-09-12 they did so with no trace, so an
// unreadable group was indistinguishable from a group with no siblings.
func TestVersionGroupSiblings_ReadErrorIsLogged(t *testing.T) {
	m := mocks.NewMockStore(t)
	m.EXPECT().GetBooksByVersionGroup("vg-1").Return(nil, errors.New("injected group read error"))

	var logs lockedLogBuffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	vg := "vg-1"
	mfs := &Service{db: m}
	require.Nil(t, mfs.librarySibling(&database.Book{ID: "b1", VersionGroupID: &vg}),
		"no sibling can be chosen from an unreadable group")
	require.Contains(t, logs.String(), "injected group read error")
	require.Contains(t, logs.String(), "library-copy lookup")
}
