// file: core/backup/journal/base_test.go
// version: 1.1.0
// guid: 7c19e4a3-5d80-4b27-a6f1-3e8d09b2c571
// last-edited: 2026-11-11

package journal

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCreateBased_StartsBelowAStoreHead: a new journal that continues after the
// store's committed head is a valid chain the store's head lies on, takes the
// next seq after it, and is created exclusively with mode 0600; a store whose
// head is not the base is still refused (a deleted journal is never a fresh
// start).
func TestCreateBased_StartsBelowAStoreHead(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)
	j := fx.open(t)
	fx.commitRevoke(t, j, "joker")
	fx.commitRevoke(t, j, "penguin")
	must(t, j.Close())
	head := fx.head(t)
	if head.Seq != 2 {
		t.Fatalf("head = %+v", head)
	}

	must(t, os.Remove(fx.path))
	hdr := Header{InstallID: installID, App: "gotham", StartedAt: time.Date(2026, 11, 10, 6, 0, 0, 0, time.UTC)}
	must(t, CreateBased(fx.path, fx.keys, hdr, head))
	if st, err := os.Stat(fx.path); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, %v; want 0600", st.Mode().Perm(), err)
	}
	if err := CreateBased(fx.path, fx.keys, hdr, head); err == nil {
		t.Fatal("CreateBased replaced an existing file")
	}
	c := fx.chain(t)
	if c.Break != nil || !c.HasHeader || c.Header.BaseSeq != 2 || c.Header.BaseMAC != head.MAC || c.IntactEnd() != 2 {
		t.Fatalf("chain = %+v, break %v", c.Header, c.Break)
	}
	if mac, ok := c.MACAt(2); !ok || mac != head.MAC {
		t.Errorf("MACAt(base) = %q, %v; want the head's mac", mac, ok)
	}

	// It opens under the store's head and numbers on from the base.
	j = fx.open(t)
	if _, err := j.AppendLine(ctx, GapLine(time.Date(2026, 11, 10, 6, 0, 0, 0, time.UTC), GapOtherInstall)); err != nil {
		t.Fatal(err)
	}
	fx.commitRevoke(t, j, "riddler")
	must(t, j.Close())
	c = fx.chain(t)
	if c.Break != nil || len(c.Lines) != 2 || c.Lines[0].Seq != 3 || c.Lines[0].Kind != Gap || c.Lines[1].Seq != 4 {
		t.Fatalf("chain after the first lines = %+v (break %v)", c.Lines, c.Break)
	}
	if h := fx.head(t); h.Seq != 4 {
		t.Errorf("head = %+v, want seq 4", h)
	}

	// A base that is not where the store's head is: refused.
	must(t, os.Remove(fx.path))
	must(t, CreateBased(fx.path, fx.keys, hdr, Head{Seq: 1, MAC: "ab"}))
	fx.openBroken(t, "a base below the head", ErrCorrupt)
	must(t, os.Remove(fx.path))
	must(t, CreateBased(fx.path, fx.keys, hdr, Head{Seq: 4, MAC: strings.Repeat("0", 64)}))
	fx.openBroken(t, "a base with another mac", ErrCorrupt)

	for name, args := range map[string]func() error{
		"no path":       func() error { return CreateBased("", fx.keys, hdr, head) },
		"no keys":       func() error { return CreateBased(filepath.Join(fx.dir, "x"), nil, hdr, head) },
		"no install ID": func() error { return CreateBased(filepath.Join(fx.dir, "x"), fx.keys, Header{}, head) },
	} {
		if err := args(); err == nil {
			t.Errorf("CreateBased accepted %s", name)
		}
	}
}

// TestChain_FingerprintAndLatch: the fingerprint of a broken journal state
// changes with the break, the head and the state tag, not with the file's
// claimed end (a forged seq would mint a new fingerprint every boot), and the
// latch stores one.
func TestChain_FingerprintAndLatch(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)
	lines, _, head := fx.built(t)
	edited := append([]string{}, lines...)
	edited[2] = strings.Replace(edited[2], `"id":"penguin"`, `"id":"alfred"`, 1)
	fx.write(t, edited...)
	broken := fx.chain(t)
	fx.write(t, lines[0], edited[1], lines[2])
	other := fx.chain(t)

	a := broken.Fingerprint(Head{Seq: 1}, "")
	claimsMore := *broken
	claimsMore.ClaimedEnd = math.MaxUint64
	for name, b := range map[string]string{
		"another head":       broken.Fingerprint(Head{Seq: 2}, ""),
		"forked":             broken.Fingerprint(Head{Seq: 1}, "forked"),
		"another break":      other.Fingerprint(Head{Seq: 1}, ""),
		"an intact chain":    (&Chain{}).Fingerprint(head, ""),
		"the same, repeated": a,
		"a longer claim":     claimsMore.Fingerprint(Head{Seq: 1}, ""),
	} {
		same := name == "the same, repeated" || name == "a longer claim"
		if (b == a) != same {
			t.Errorf("%s: fingerprint equal = %v, want %v", name, b == a, same)
		}
	}

	if got, err := ReadBrokenLatch(ctx, fx.ks); got != "" || err != nil {
		t.Fatalf("latch before = %q, %v", got, err)
	}
	must(t, WriteBrokenLatch(ctx, fx.ks, a, time.Date(2026, 11, 10, 7, 0, 0, 0, time.UTC)))
	if got, err := ReadBrokenLatch(ctx, fx.ks); got != a || err != nil {
		t.Fatalf("latch = %q, %v; want %q", got, err, a)
	}
}
