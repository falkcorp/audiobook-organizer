// file: core/library/hash.go
// version: 1.0.0
// guid: c56e37d1-9700-479b-b762-71caca0f99ae
// last-edited: 2026-11-11

package library

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"sync"
	"time"

	"github.com/falkcorp/mediacore/internal/libfs"
)

// Hash reads the file once through its root, hashing and sizing the same
// descriptor, so the size describes exactly the bytes the digest covers. It
// stops at a 1 MiB boundary when ctx is done and honours
// core.library.hash.max_bytes_per_sec. Identity hashes with module-specific
// algorithms stay in the module and use RootFS.Open.
func (m *manager) Hash(ctx context.Context, p Path) (Digest, error) {
	if p.IsRoot() {
		return Digest{}, fmt.Errorf("%w: a root directory has no digest", ErrBadTransfer)
	}
	r, err := m.need(p)
	if err != nil {
		return Digest{}, err
	}
	d, err := m.hashVia(ctx, r.rw, p.Rel, true)
	if err == nil {
		m.ctr.addHashBytes(r.spec.ID, d.Size)
	}
	return d, err
}

// hashVia hashes rel of fsys: the digest and the byte count come from one open
// file. throttled applies the byte-rate limit (the transfer's own verify reads
// are not rate-limited: they are part of a write the caller asked for).
func (m *manager) hashVia(ctx context.Context, fsys fs.FS, rel string, throttled bool) (Digest, error) {
	f, err := fsys.Open(rel)
	if err != nil {
		return Digest{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return Digest{}, err
	}
	if !st.Mode().IsRegular() {
		return Digest{}, fmt.Errorf("%w: %s is not a regular file", ErrNotSupported, rel)
	}
	h := sha256.New()
	o := libfs.CopyOptions{Hash: h, Check: ctx.Err}
	if throttled {
		o.Throttle = func(n int) error { return m.lim.wait(ctx, m.seam, m.reg.Load().set.hashRate, n) }
	}
	n, err := libfs.Copy(io.Discard, f, o)
	if err != nil {
		return Digest{}, err
	}
	return Digest{SHA256: hex.EncodeToString(h.Sum(nil)), Size: n}, nil
}

// limiter is one shared byte-rate limit: every hashing caller advances the same
// clock, so the cap is on the manager, not on each call.
type limiter struct {
	mu   sync.Mutex
	next time.Time
}

// wait charges n bytes at rate bytes per second (0 = unlimited) and sleeps until
// the clock allows them, or ctx is done.
func (l *limiter) wait(ctx context.Context, sm seams, rate int64, n int) error {
	if rate <= 0 {
		return ctx.Err()
	}
	l.mu.Lock()
	now := sm.now()
	if l.next.Before(now) {
		l.next = now
	}
	delay := l.next.Sub(now)
	l.next = l.next.Add(time.Duration(float64(n) / float64(rate) * float64(time.Second)))
	l.mu.Unlock()
	if delay <= 0 {
		return ctx.Err()
	}
	return sm.sleepFor(ctx, delay)
}

// sleepFor sleeps d or until ctx is done.
func (s seams) sleepFor(ctx context.Context, d time.Duration) error {
	if s.sleep != nil {
		return s.sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
