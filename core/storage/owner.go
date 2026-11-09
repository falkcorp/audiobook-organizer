// file: core/storage/owner.go
// version: 1.0.0
// guid: 5bfe4aa9-c32f-44ac-8868-5b0994333003
// last-edited: 2026-11-09

package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"strconv"
	"strings"

	"github.com/falkcorp/mediacore/internal/bootseam"
)

// ErrNotStateDirOwner: a command that writes the store, the state
// directory or the checkpoint directory runs as a user other than the state
// directory's owner, the user the service runs as. What it would leave
// behind (the swap's journal directories, created 0700; the state files a
// restore puts back, 0600; an extracted store; the files Pebble writes; an
// --into copy, the scratch area, the checkpoint directory itself when it
// did not exist yet, and the sweep lock) would belong to that other user,
// root most often, and the service could not read, replace or sweep them:
// its next boot would fail, its next upgrade would fail at the checkpoint
// it takes first, or its checkpoint sweeps would stop (R1-25 review B-1).
// Run the command as the owner: the error names the sudo -u line.
var ErrNotStateDirOwner = errors.New("storage: this process is not the state directory's owner")

// RefuseNotStateDirOwner refuses a command (what names it) that writes the
// store, the state directory or the checkpoint directory when its effective
// uid (bootseam.Euid: os.Geteuid in production) is not the uid that owns
// stateDir. Each caller runs it first, before it builds a kernel, takes a
// lock or reads the slot, so a refused command has touched nothing: the
// kernel's offline restore, reset and read-write open, storage
// prune-checkpoints, and ABO's diagnostics reserved-prefs writes. Only on
// the real filesystem (StateDirOwner); an empty stateDir is the caller's to
// refuse, or has nothing to check.
func RefuseNotStateDirOwner(ctx context.Context, stateDir, what string) error {
	if stateDir == "" {
		return nil
	}
	owner, ok, err := StateDirOwner(bootseam.FS(ctx), stateDir)
	if err != nil {
		return fmt.Errorf("storage: %s: %w", what, err)
	}
	euid := bootseam.Euid(ctx)
	if !ok || euid == int(owner) {
		return nil
	}
	id := strconv.FormatUint(uint64(owner), 10)
	name := "#" + id
	if u, err := user.LookupId(id); err == nil && u.Username != "" {
		name = u.Username
	}
	return fmt.Errorf("%w: %s writes files that the service, which runs as the state directory's owner %s (uid %d), must read and replace, and this process runs as uid %d; run it as that user: sudo -u %s %s",
		ErrNotStateDirOwner, what, name, owner, euid, shellQuote(name), commandLine())
}

// commandLine is this process's command line, quoted for a shell.
func commandLine() string {
	quoted := make([]string, len(os.Args))
	for i, a := range os.Args {
		quoted[i] = shellQuote(a)
	}
	return strings.Join(quoted, " ")
}

// shellQuote returns s as one shell word: as it is when it holds only
// characters no shell treats specially, else in single quotes.
func shellQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-./=:,@%+") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
