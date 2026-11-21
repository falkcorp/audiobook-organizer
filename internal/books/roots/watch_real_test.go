// file: internal/books/roots/watch_real_test.go
// version: 0.1.0
// guid: 6c2d9a1e-4b7f-4e03-8a5c-9d1f3b7e2a64
// last-edited: 2026-11-20

package roots_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/library"

	"github.com/falkcorp/audiobook-organizer/internal/books/roots"
)

// The Watcher over a real Scope: what fakeSource assumed about library.Scope.Watch
// (a watch-off root is refused with ErrNotSupported, a real event reaches the
// handler with a root-relative directory, and cancelling ends the sequence so
// Stop returns) is checked here.
func TestWatcher_OverARealScope(t *testing.T) {
	rg := newRig(t, nil)
	ctx := context.Background()

	if _, err := rg.books.Watch(ctx, "import-2", library.WatchOptions{}); !errors.Is(err, library.ErrNotSupported) {
		t.Fatalf("Watch on a watch-off root = %v; want ErrNotSupported", err)
	}

	cs := &changes{}
	d := &desiredSet{}
	d.set("import-1")
	w, err := roots.NewWatcher(roots.WatcherOptions{
		Source: rg.books, Handler: cs.handle, Desired: d.get,
		Extensions: []string{".m4b"}, Debounce: 20 * time.Millisecond,
		Interval: time.Hour, ResubscribeDelay: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.Stop)
	w.Reconcile(ctx)

	book := filepath.Join(rg.arkham, "Hush")
	if err := os.MkdirAll(book, 0o755); err != nil {
		t.Fatal(err)
	}
	// Keep writing until a change arrives: the watch is subscribed on its own
	// goroutine, so the first write can precede it.
	deadline := time.Now().Add(10 * time.Second)
	for i := 0; len(cs.all()) == 0; i++ {
		if time.Now().After(deadline) {
			t.Fatal("no change arrived from a real watch")
		}
		_ = os.WriteFile(filepath.Join(book, "part"+string(rune('a'+i%26))+".m4b"), []byte("x"), 0o644)
		time.Sleep(50 * time.Millisecond)
	}
	c := cs.all()[0]
	if c.Root != "import-1" || len(c.Dirs) == 0 {
		t.Fatalf("change = %+v", c)
	}
	for _, p := range c.Dirs {
		if p.Root != "import-1" {
			t.Errorf("dir %+v is not under import-1", p)
		}
		if !c.Rescan && p.Rel != "Hush" {
			t.Errorf("dir %+v; want the changed directory Hush", p)
		}
	}

	stopped := make(chan struct{})
	go func() { w.Stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("Stop did not return: the real watch sequence did not end on cancel")
	}
	if len(w.Watched()) != 0 {
		t.Errorf("Watched after Stop = %v", w.Watched())
	}
}

// A root library will not watch is retried by Reconcile only, and does not leave
// a goroutine behind.
func TestWatcher_RealWatchOffRootEndsItsWatch(t *testing.T) {
	rg := newRig(t, nil)
	d := &desiredSet{}
	d.set("import-2")
	var errs []error
	w, err := roots.NewWatcher(roots.WatcherOptions{
		Source: rg.books, Handler: (&changes{}).handle, Desired: d.get,
		OnError: func(err error) { errs = append(errs, err) }, Interval: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	w.Reconcile(context.Background())
	waitFor(t, "the watch to end", func() bool { return len(w.Watched()) == 0 })
	w.Stop() // joins the goroutine, so errs is safe to read after it
	if len(errs) != 1 || !errors.Is(errs[0], library.ErrNotSupported) {
		t.Errorf("OnError = %v; want one ErrNotSupported", errs)
	}
}
