// file: core/kernel/owner.go
// version: 1.0.0
// guid: 1fa90151-0c86-437d-9058-cfe25186b53b
// last-edited: 2026-11-09

package kernel

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"strconv"
	"strings"

	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/storage"
)

// ErrNotStateDirOwner: an offline command that writes the store or the
// state directory runs as a user other than the state directory's owner,
// the user the service runs as. What it would leave behind (the swap's
// journal directories, created 0700; the state files a restore puts back,
// 0600; an extracted store; the files Pebble writes) would belong to that
// other user, root most often, and the service could not read or replace
// them, so its next boot would fail (R1-25 review B-1). Run the command as
// the owner: the error names the sudo -u line.
var ErrNotStateDirOwner = errors.New("kernel: this process is not the state directory's owner")

// refuseNotOwner refuses an offline command (what names it) whose effective
// uid (bootseam.Euid: os.Geteuid in production) is not the uid that owns
// stateDir. It runs before the command builds its kernel, takes a lock or
// reads the slot, so a refused command has touched nothing. Only on the
// real filesystem (storage.StateDirOwner); an empty stateDir is
// offlineKernel's refusal.
func refuseNotOwner(ctx context.Context, stateDir, what string) error {
	if stateDir == "" {
		return nil
	}
	owner, ok, err := storage.StateDirOwner(bootseam.FS(ctx), stateDir)
	if err != nil {
		return fmt.Errorf("kernel: %s: %w", what, err)
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
