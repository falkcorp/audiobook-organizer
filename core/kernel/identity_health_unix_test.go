// file: core/kernel/identity_health_unix_test.go
// version: 0.1.0
// guid: 7e3c9a15-8b42-4d60-b5f1-0a6d2e94c837
// last-edited: 2026-11-21

//go:build unix

package kernel

import (
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/falkcorp/mediacore/backup/journal"
)

// TestHealth_RevocationJournalBrokenAtBootAlsoNamesUnwritable: a journal that
// cannot be opened at boot (here: another open file description holds its lock,
// as a second process running the same install would) is both broken (the boot
// noted it) and unwritable (it takes no line). core.backup carries one Reason,
// the higher-ranked revocation_journal_broken, and Detail lists the unwritable
// token too; the boot still serves and the unauthenticated surface carries
// neither.
func TestHealth_RevocationJournalBrokenAtBootAlsoNamesUnwritable(t *testing.T) {
	h, c := identityHost(t)
	first := h.mustBoot(c)
	path := journalPath(first)
	h.close(first)

	held, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Close() })
	if err := syscall.Flock(int(held.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}

	k := h.mustBoot(c)
	requirePosture(t, k, componentBackup, journal.HealthBroken)
	cs, _ := componentState(t, k, componentBackup)
	if !strings.Contains(cs.Result.Detail, journal.HealthUnwritable+": ") {
		t.Errorf("Detail %q does not name %s", cs.Result.Detail, journal.HealthUnwritable)
	}
}
