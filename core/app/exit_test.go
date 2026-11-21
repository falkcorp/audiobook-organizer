// file: core/app/exit_test.go
// version: 0.1.0
// guid: 3f0b6c1e-7d52-4a98-b1a4-c5e96d2f8a07
// last-edited: 2026-11-21

package app

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/storage"
)

// exitEnv is a getenv over a fixed map.
func exitEnv(env map[string]string) func(string) string {
	return func(k string) string { return env[k] }
}

// sleepRecorder records every injected sleep, so no test waits.
type sleepRecorder struct{ slept []time.Duration }

func (r *sleepRecorder) sleep(d time.Duration) { r.slept = append(r.slept, d) }

func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return lines[len(lines)-1]
}

// TestExitStatusUpgradeFloor: a floor refusal exits 78 and its LAST stderr
// line is exactly "install <tag> first" (R3-02 I-floor-1), wrapped or not,
// after the error text itself.
func TestExitStatusUpgradeFloor(t *testing.T) {
	floor := &storage.ErrUpgradeFloor{Owner: "gcpd", Have: 12, Floor: 66, InstallFirst: "v9.9.9"}
	for name, err := range map[string]error{
		"plain":   floor,
		"wrapped": fmt.Errorf("kernel: P4: %w", floor),
		// storage's boot returns errors.Join(every refusal), so a floor
		// refusal arrives beside the others.
		"joined": fmt.Errorf("kernel: boot step %q: %w", "storage", errors.Join(errors.New("the Penguin's umbrella"), floor)),
	} {
		t.Run(name, func(t *testing.T) {
			var stderr bytes.Buffer
			var rec sleepRecorder
			got := ExitStatus(err, &stderr, exitEnv(nil), rec.sleep)
			if got != 78 {
				t.Fatalf("ExitStatus = %d, want 78", got)
			}
			if want := "install v9.9.9 first"; lastLine(stderr.String()) != want {
				t.Errorf("last stderr line = %q, want %q (stderr %q)", lastLine(stderr.String()), want, stderr.String())
			}
			if !strings.HasPrefix(stderr.String(), err.Error()+"\n") {
				t.Errorf("stderr does not start with the error text: %q", stderr.String())
			}
			if len(rec.slept) != 0 {
				t.Errorf("slept %v without launchd", rec.slept)
			}
		})
	}
}

// TestExitStatusFloorWithoutTag: a floor that names no release prints no
// "install  first" line: the error text stays the last line.
func TestExitStatusFloorWithoutTag(t *testing.T) {
	err := &storage.ErrUpgradeFloor{Owner: "gcpd", Have: 12, Floor: 66}
	var stderr bytes.Buffer
	if got := ExitStatus(err, &stderr, exitEnv(nil), (&sleepRecorder{}).sleep); got != 78 {
		t.Fatalf("ExitStatus = %d, want 78", got)
	}
	if stderr.String() != err.Error()+"\n" {
		t.Errorf("stderr = %q, want just the error text", stderr.String())
	}
}

// TestFloorLaunchdSleepsBeforeExit78: under launchd the refusal sleeps 60 s
// (once) before it returns 78, so launchd's relaunch cannot hot-loop; under
// systemd, in Docker, or from a terminal it returns at once (I-floor-4).
func TestFloorLaunchdSleepsBeforeExit78(t *testing.T) {
	floor := &storage.ErrUpgradeFloor{Owner: "gcpd", Have: 12, Floor: 66, InstallFirst: "v9.9.9"}
	for name, c := range map[string]struct {
		env  map[string]string
		want []time.Duration
	}{
		"launchd job":          {map[string]string{"XPC_SERVICE_NAME": "com.gotham.batcave"}, []time.Duration{60 * time.Second}},
		"systemd":              {map[string]string{"INVOCATION_ID": "0123456789abcdef"}, nil},
		"unset":                {nil, nil},
		"empty":                {map[string]string{"XPC_SERVICE_NAME": ""}, nil},
		"terminal sets zero":   {map[string]string{"XPC_SERVICE_NAME": "0"}, nil},
		"launchd plus systemd": {map[string]string{"XPC_SERVICE_NAME": "com.gotham.batcave", "INVOCATION_ID": "x"}, []time.Duration{60 * time.Second}},
	} {
		t.Run(name, func(t *testing.T) {
			var stderr bytes.Buffer
			var rec sleepRecorder
			got := ExitStatus(floor, &stderr, exitEnv(c.env), rec.sleep)
			if got != 78 {
				t.Fatalf("ExitStatus = %d, want 78", got)
			}
			if fmt.Sprint(rec.slept) != fmt.Sprint(c.want) {
				t.Errorf("slept %v, want %v", rec.slept, c.want)
			}
			if lastLine(stderr.String()) != "install v9.9.9 first" {
				t.Errorf("last line %q: the line must be written before the sleep, whatever the supervisor", lastLine(stderr.String()))
			}
		})
	}
}

// TestExitStatusLineWrittenBeforeSleep: an operator watching a launchd job's
// log sees the instruction during the 60 s wait, not after it.
func TestExitStatusLineWrittenBeforeSleep(t *testing.T) {
	floor := &storage.ErrUpgradeFloor{Owner: "gcpd", Have: 12, Floor: 66, InstallFirst: "v9.9.9"}
	var stderr bytes.Buffer
	var atSleep string
	ExitStatus(floor, &stderr, exitEnv(map[string]string{"XPC_SERVICE_NAME": "com.gotham.batcave"}), func(time.Duration) {
		atSleep = stderr.String()
	})
	if lastLine(atSleep) != "install v9.9.9 first" {
		t.Errorf("at the sleep stderr was %q, want the install line already written", atSleep)
	}
}

// TestExitStatusOtherArms: every other error keeps its code and its single
// stderr line; the other 78 refusals sleep under launchd too (launchd cannot
// tell them apart, and a restart cannot help any of them) but get no
// "install first" line; ordinary failures never sleep.
func TestExitStatusOtherArms(t *testing.T) {
	launchd := map[string]string{"XPC_SERVICE_NAME": "com.gotham.batcave"}
	for name, c := range map[string]struct {
		err       error
		want      int
		wantSlept int
	}{
		"nil":           {nil, 0, 0},
		"blocked":       {fmt.Errorf("kernel: P4a: %w", &storage.CutoverBlockedError{}), 78, 1},
		"swapped back":  {fmt.Errorf("storage: M8: %w", storage.ErrRewriteSwappedBack), 78, 1},
		"level too new": {&storage.LevelTooNewError{Owner: "gcpd", Store: 3, Supported: 2, Source: "stamp"}, 78, 1},
		"ordinary":      {errors.New("the Riddler's question"), 1, 0},
		"quarantine":    {storage.ErrQuarantineNoSealer, 1, 0},
	} {
		t.Run(name, func(t *testing.T) {
			var stderr bytes.Buffer
			var rec sleepRecorder
			got := ExitStatus(c.err, &stderr, exitEnv(launchd), rec.sleep)
			if got != c.want {
				t.Errorf("ExitStatus = %d, want %d", got, c.want)
			}
			if len(rec.slept) != c.wantSlept {
				t.Errorf("slept %v, want %d sleeps", rec.slept, c.wantSlept)
			}
			if c.err == nil {
				if stderr.Len() != 0 {
					t.Errorf("nil error wrote %q", stderr.String())
				}
				return
			}
			if want := c.err.Error() + "\n"; stderr.String() != want {
				t.Errorf("stderr = %q, want exactly the error line %q", stderr.String(), want)
			}
		})
	}
}

// (failingWriter, a writer that refuses every write, is extendstart_test.go's.)

// TestExitStatusStderrFailure: a stderr that cannot be written does not
// change the exit status or skip the launchd wait.
func TestExitStatusStderrFailure(t *testing.T) {
	floor := &storage.ErrUpgradeFloor{Owner: "gcpd", Have: 12, Floor: 66, InstallFirst: "v9.9.9"}
	var rec sleepRecorder
	got := ExitStatus(floor, failingWriter{}, exitEnv(map[string]string{"XPC_SERVICE_NAME": "com.gotham.batcave"}), rec.sleep)
	if got != 78 || len(rec.slept) != 1 {
		t.Errorf("ExitStatus = %d with %d sleeps, want 78 with 1", got, len(rec.slept))
	}
}
