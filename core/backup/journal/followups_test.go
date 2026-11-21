// file: core/backup/journal/followups_test.go
// version: 0.1.0
// guid: b4a08403-1b5f-4cd1-aa61-5dca0cb3067e
// last-edited: 2026-11-20

package journal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/storage"
)

// The F4-11 follow-ups F4-08 owns (TODO F4-11-F4-08-REFUSE and -QUEUE): the
// header's app is compared when the journal is opened, the health tokens carry
// no file path, and a non-empty unapplied queue has a token of its own.

// TestOpenFile_AnotherAppsJournalIsRefusedUntouched: a journal whose header names
// another app than the one opening it is refused (ErrOtherApp), the way another
// install's is. Nothing is written to it: not a header, not the cut of an
// incomplete last line an open of this app's journal would make. The broken File
// refuses every append and stages nothing, so the revoking caller records a gap.
func TestOpenFile_AnotherAppsJournalIsRefusedUntouched(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)
	j := fx.open(t)
	fx.commitRevoke(t, j, "joker")
	must(t, j.Close())
	f, err := os.OpenFile(fx.path, os.O_APPEND|os.O_WRONLY, 0)
	must(t, err)
	_, err = f.WriteString(`{"seq":2,"kind":"user.dis`)
	must(t, errors.Join(err, f.Close()))
	before, err := os.ReadFile(fx.path)
	must(t, err)

	o := fx.opts()
	o.App = "bludhaven"
	oj, err := OpenFile(ctx, o)
	if !errors.Is(err, ErrOtherApp) || oj == nil {
		if oj != nil {
			_ = oj.Close()
		}
		t.Fatalf("OpenFile of the gotham journal as bludhaven = %v, %v; want a broken File and ErrOtherApp", oj, err)
	}
	defer func() { must(t, oj.Close()) }()
	b := fx.auth.NewBatch()
	must(t, b.Set([]byte("user/bane"), []byte("disabled")))
	if err := oj.Append(ctx, b, Revocation{Kind: UserDisabled}); !errors.Is(err, ErrOtherApp) {
		t.Errorf("Append on another app's journal: err = %v, want ErrOtherApp", err)
	}
	if b.Count() != 1 {
		t.Errorf("the refused Append staged into the batch: count %d", b.Count())
	}
	after, err := os.ReadFile(fx.path)
	must(t, err)
	if !bytes.Equal(before, after) {
		t.Errorf("another app's journal was written to:\nbefore %q\nafter  %q", before, after)
	}
}

// TestComponent_AnotherAppIsRevocationJournalBroken: the component built for one
// app and bound to another app's journal reports revocation_journal_broken
// (degraded), not unwritable, and refuses appends.
func TestComponent_AnotherAppIsRevocationJournalBroken(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)
	j := fx.open(t)
	must(t, j.Close())
	c := fx.componentWith(t, func(o *ComponentOptions) { o.App = "bludhaven" })
	if err := c.Bind(ctx, fx.path); !errors.Is(err, ErrOtherApp) {
		t.Fatalf("Bind = %v, want ErrOtherApp", err)
	}
	if h, ok := hasHealth(c.Health(), HealthBroken); !ok || h.Severity != SeverityDegraded {
		t.Errorf("health = %+v, want %s degraded", c.Health(), HealthBroken)
	}
	if _, ok := hasHealth(c.Health(), HealthUnwritable); ok {
		t.Errorf("another app's journal is broken, not unwritable: %+v", c.Health())
	}
	if err := c.Append(ctx, fx.auth.NewBatch(), Revocation{Kind: UserDeleted}); !errors.Is(err, ErrOtherApp) {
		t.Errorf("Append = %v, want ErrOtherApp", err)
	}
	if c.App() != "bludhaven" {
		t.Errorf("App() = %q, want the component's own app", c.App())
	}
}

// TestComponent_HealthDetailNamesNoPath: every token's Detail is a fixed
// sentence or a count. The errors behind them name the journal file (an open
// failure does, a write failure does), and a health report is no place for a
// file path (F4-08 Q6c): the boot that records the condition logs the error.
// Each branch of Health is driven by an error that carries the path.
func TestComponent_HealthDetailNamesNoPath(t *testing.T) {
	ctx := context.Background()
	pathErr := func(fx *fixture, what string) error { return fmt.Errorf("%s %s: %w", what, fx.path, syscall.ENOSPC) }
	cases := []struct {
		name  string
		token string
		raise func(t *testing.T, fx *fixture) *Component
	}{
		{"a break the boot noted", HealthBroken, func(t *testing.T, fx *fixture) *Component {
			c := fx.bound(t)
			c.NoteBroken(pathErr(fx, "the chain breaks in"))
			return c
		}},
		{"a chain that does not verify at Bind", HealthBroken, func(t *testing.T, fx *fixture) *Component {
			j := fx.open(t)
			fx.commitRevoke(t, j, "joker")
			must(t, j.Close())
			lines := fx.lines(t)
			fx.write(t, lines[0], strings.Replace(lines[1], `"id":"joker"`, `"id":"alfred"`, 1))
			c := fx.component(t)
			if err := c.Bind(ctx, fx.path); !errors.Is(err, ErrCorrupt) || !strings.Contains(err.Error(), fx.path) {
				t.Fatalf("Bind = %v, want ErrCorrupt naming the file (the case needs a path in the error)", err)
			}
			return c
		}},
		{"another install's journal at Bind", HealthBroken, func(t *testing.T, fx *fixture) *Component {
			o := fx.opts()
			o.InstallID = "arkham-install"
			must(t, os.MkdirAll(filepath.Dir(fx.path), 0o700))
			oj, err := OpenFile(ctx, o)
			must(t, err)
			must(t, oj.Close())
			c := fx.component(t)
			if err := c.Bind(ctx, fx.path); !errors.Is(err, ErrOtherInstall) || !strings.Contains(err.Error(), fx.path) {
				t.Fatalf("Bind = %v, want ErrOtherInstall naming the file", err)
			}
			return c
		}},
		{"another app's journal at Bind", HealthBroken, func(t *testing.T, fx *fixture) *Component {
			j := fx.open(t)
			must(t, j.Close())
			c := fx.componentWith(t, func(o *ComponentOptions) { o.App = "bludhaven" })
			if err := c.Bind(ctx, fx.path); !errors.Is(err, ErrOtherApp) || !strings.Contains(err.Error(), fx.path) {
				t.Fatalf("Bind = %v, want ErrOtherApp naming the file", err)
			}
			return c
		}},
		{"a write that failed", HealthUnwritable, func(t *testing.T, fx *fixture) *Component {
			c := fx.bound(t)
			c.file.mu.Lock()
			c.file.broken = pathErr(fx, "write")
			c.file.mu.Unlock()
			return c
		}},
		{"a hold", HealthUnwritable, func(t *testing.T, fx *fixture) *Component {
			c := fx.bound(t)
			c.Hold(pathErr(fx, "the restore line was not written to"))
			return c
		}},
		{"an open gap", HealthUnwritable, func(t *testing.T, fx *fixture) *Component {
			// A gap recorded before Bind is adopted as open by it.
			c := fx.component(t)
			b := fx.auth.NewBatch()
			must(t, c.Gap(b, time.Date(2026, 11, 10, 4, 0, 0, 0, time.UTC)))
			must(t, b.Commit(ctx, storage.Sync))
			must(t, os.MkdirAll(filepath.Dir(fx.path), 0o700))
			must(t, c.Bind(ctx, fx.path))
			return c
		}},
		{"a reset", HealthReset, func(t *testing.T, fx *fixture) *Component {
			c := fx.bound(t)
			c.NoteReset()
			return c
		}},
		{"colocation", HealthColocated, func(t *testing.T, fx *fixture) *Component {
			c := fx.bound(t)
			c.o.DirDev = func(string) (uint64, error) { return 42, nil }
			return c
		}},
		{"a waiting unapplied queue", HealthQueued, func(t *testing.T, fx *fixture) *Component {
			c := fx.bound(t)
			c.NoteUnapplied(3)
			return c
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newFixture(t)
			c := tc.raise(t, fx)
			h, ok := hasHealth(c.Health(), tc.token)
			if !ok {
				t.Fatalf("health = %+v, want %s", c.Health(), tc.token)
			}
			for _, leak := range []string{fx.dir, fx.path, filepath.Base(fx.path), string(filepath.Separator)} {
				if strings.Contains(h.Detail, leak) {
					t.Errorf("%s Detail %q carries %q: a health report names no file path", h.Token, h.Detail, leak)
				}
			}
			if h.Detail == "" {
				t.Errorf("%s has an empty Detail", h.Token)
			}
			for _, other := range c.Health() {
				for _, leak := range []string{fx.dir, string(filepath.Separator)} {
					if strings.Contains(other.Detail, leak) {
						t.Errorf("%s Detail %q carries %q", other.Token, other.Detail, leak)
					}
				}
			}
		})
	}
}

// TestComponent_QueuedTokenIsTheCountOnly: NoteUnapplied with entries waiting
// raises revocation_journal_queued at info severity, with the count and nothing
// of an entry (its kind is a newer build's string); zero clears it.
func TestComponent_QueuedTokenIsTheCountOnly(t *testing.T) {
	fx := newFixture(t)
	c := fx.bound(t)
	if _, ok := hasHealth(c.Health(), HealthQueued); ok {
		t.Fatalf("a component nothing was noted on reports %s: %+v", HealthQueued, c.Health())
	}
	c.NoteUnapplied(2)
	h, ok := hasHealth(c.Health(), HealthQueued)
	if !ok || h.Severity != SeverityInfo || !strings.Contains(h.Detail, "2") {
		t.Errorf("health with two entries waiting = %+v, want %s info with the count", c.Health(), HealthQueued)
	}
	if _, ok := hasHealth(c.Health(), HealthBroken); ok {
		t.Errorf("a waiting queue is not a broken journal: %+v", c.Health())
	}
	if _, ok := hasHealth(c.Health(), HealthUnwritable); ok {
		t.Errorf("a waiting queue is not an unwritable journal: %+v", c.Health())
	}
	c.NoteUnapplied(0)
	if _, ok := hasHealth(c.Health(), HealthQueued); ok {
		t.Errorf("an empty queue still reports %s: %+v", HealthQueued, c.Health())
	}
	// It is the component's state, not the file's: it needs no Bind.
	u := fx.component(t)
	u.NoteUnapplied(1)
	if _, ok := hasHealth(u.Health(), HealthQueued); !ok {
		t.Errorf("an unbound component dropped the queue count: %+v", u.Health())
	}
}

// TestHealthTokensArePostureTokens: every token the component reports carries
// the revocation_journal_ prefix, which core/health's posture filter
// (isPostureReason) generalises to "<component>:down" on a public surface, and
// is pinned in its TestPostureTokensGolden. A token outside the prefix would
// reach /readyz in clear.
func TestHealthTokensArePostureTokens(t *testing.T) {
	for _, tok := range []string{HealthUnwritable, HealthBroken, HealthReset, HealthColocated, HealthQueued} {
		if !strings.HasPrefix(tok, "revocation_journal_") || len(tok) > 40 || strings.ToLower(tok) != tok {
			t.Errorf("%q is not a revocation_journal_ reason token of at most 40 lower-case characters", tok)
		}
	}
}
