// file: core/app/exit.go
// version: 0.1.0
// guid: 9a4e1d70-2c85-4f36-8b17-d03e5c7a6b21
// last-edited: 2026-11-21

package app

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/falkcorp/mediacore/kernel"
	"github.com/falkcorp/mediacore/storage"
)

// launchdBackoff is how long a refusal waits, under launchd, before it
// exits 78. launchd has no per-exit-code restart setting (systemd's is
// RestartPreventExitStatus=78), so it relaunches a 78 exit like any other
// failure, throttled to once every ten seconds by default; the wait stretches
// that to once a minute so the refusal does not hot-loop while the operator
// reads the log (R3-02 I-floor-4; 09 §9.2.2).
const launchdBackoff = 60 * time.Second

// launchdServiceEnv is the variable launchd sets to a job's label for every
// process the job starts.
const launchdServiceEnv = "XPC_SERVICE_NAME"

// ExitStatus is the host's process exit for a boot's error: it writes err to
// stderr as one line and returns the exit status kernel.ExitCode maps it to,
// which the caller passes to os.Exit (a host's run function returns it).
//
//   - nil writes nothing and returns 0.
//   - A store below a declared upgrade floor (*storage.ErrUpgradeFloor)
//     writes one more line after the error, "install <tag> first" with the
//     tag the floor names, so it is the LAST line of stderr (the line a
//     Docker log shows, and the one the release note tells the operator to
//     look for). A floor that names no release writes no such line.
//   - Under launchd, a refusal that exits 78 (kernel.ExitConfig: the floor,
//     a blocked cutover, a store too new) calls sleep(60 s) after the lines
//     are written and before it returns, so launchd's relaunch cannot
//     hot-loop. Under systemd (RestartPreventExitStatus=78), Docker or a
//     terminal it returns at once. Any other failure never waits.
//
// stderr, getenv and sleep are injectable so a test neither prints nor
// waits; nil means os.Stderr, os.Getenv and time.Sleep.
func ExitStatus(err error, stderr io.Writer, getenv func(string) string, sleep func(time.Duration)) int {
	if err == nil {
		return 0
	}
	if stderr == nil {
		stderr = os.Stderr
	}
	if getenv == nil {
		getenv = os.Getenv
	}
	if sleep == nil {
		sleep = time.Sleep
	}
	code := kernel.ExitCode(err)
	writeLine(stderr, err.Error())
	if floor, ok := errors.AsType[*storage.ErrUpgradeFloor](err); ok && floor.InstallFirst != "" {
		writeLine(stderr, fmt.Sprintf("install %s first", floor.InstallFirst))
	}
	if code == kernel.ExitConfig && underLaunchd(getenv) {
		sleep(launchdBackoff)
	}
	return code
}

// writeLine writes one line to w. A failed write has no better channel left
// to report on (w is the process's stderr), so it goes to the process's
// default logger, which a booted host has pointed at its configured sinks, and
// the exit status and the launchd wait carry on regardless.
func writeLine(w io.Writer, line string) {
	if _, err := io.WriteString(w, line+"\n"); err != nil {
		slog.Error("app: the exit report could not be written to stderr", "err", err)
	}
}

// underLaunchd reports whether the process was started by a launchd job.
// XPC_SERVICE_NAME is the job's label there. A process started from a
// terminal session inherits it as "0" (and one outside launchd leaves it
// unset or empty), so those are not a job: a manual run of a refused binary
// must not stall for a minute.
func underLaunchd(getenv func(string) string) bool {
	v := getenv(launchdServiceEnv)
	return v != "" && v != "0"
}
