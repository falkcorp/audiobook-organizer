// file: core/updater/selftest.go
// version: 1.0.0
// guid: c3f81a52-9d47-4e06-b1a8-5e7290d4f6b3
// last-edited: 2026-11-18

package updater

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// probeTimeout bounds one run of a downloaded or previous binary
// (--version-json, --print-storage-levels): 10 s, as 05-updater 5U.4 step 3.
const probeTimeout = 10 * time.Second

// maxProbeOutput bounds what a probe may print; a binary that prints more is
// not answering the question.
const maxProbeOutput = 64 << 10

// versionFields is the field list of --version-json (FE6-4): exactly these
// five, all always present, all strings. commit may be "".
func versionFields() []string {
	return []string{"app", "version", "commit", "ui_build_id", "binary_id"}
}

// probeResult is one finished run of a binary.
type probeResult struct {
	Stdout string
	Stderr string
	Code   int
}

// runBinary runs exe with args under probeTimeout, with an empty environment
// except PATH, and returns what it printed and its exit status. A binary that
// cannot be started is an error; one that exits non-zero is a result.
func runBinary(ctx context.Context, exe string, args ...string) (probeResult, error) {
	var res probeResult
	var err error
	// A binary written moments ago can still be "text file busy" while another
	// goroutine's fork holds the descriptor it was written through; that
	// clears at once, so a few short retries.
	for attempt := range 4 {
		res, err = runBinaryOnce(ctx, exe, args...)
		if err == nil || !errors.Is(err, syscall.ETXTBSY) {
			return res, err
		}
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 50 * time.Millisecond):
		}
	}
	return res, err
}

func runBinaryOnce(ctx context.Context, exe string, args ...string) (probeResult, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	var out, errb limitedBuffer
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	cmd.Stdout, cmd.Stderr = &out, &errb
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	res := probeResult{Stdout: out.String(), Stderr: errb.String()}
	var ee *exec.ExitError
	switch {
	case err == nil:
		return res, nil
	case errors.As(err, &ee):
		if ctx.Err() != nil {
			return res, fmt.Errorf("timed out after %s: %w", probeTimeout, ctx.Err())
		}
		res.Code = ee.ExitCode()
		return res, nil
	default:
		return res, err
	}
}

// limitedBuffer keeps the first maxProbeOutput bytes and drops the rest.
type limitedBuffer struct{ b bytes.Buffer }

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := maxProbeOutput - l.b.Len(); room > 0 {
		l.b.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

func (l *limitedBuffer) String() string { return l.b.String() }

var _ io.Writer = (*limitedBuffer)(nil)

// selfTest runs `<exe> --version-json` and requires its one JSON object to
// carry exactly {app, version, commit, ui_build_id, binary_id}, all strings,
// with app == appID and version == want (the leading "v" is optional on both
// sides). A failure is ErrSelfTest with the reason; the output is never put in
// the error, only its field names and the two compared values.
func selfTest(ctx context.Context, exe, appID, want string) error {
	res, err := runBinary(ctx, exe, "--version-json")
	if err != nil {
		return fmt.Errorf("%w: cannot run the binary: %v", ErrSelfTest, err)
	}
	if res.Code != 0 {
		return fmt.Errorf("%w: --version-json exited %d", ErrSelfTest, res.Code)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(res.Stdout)), &doc); err != nil {
		return fmt.Errorf("%w: --version-json did not print one JSON object", ErrSelfTest)
	}
	fields := versionFields()
	if len(doc) != len(fields) {
		return fmt.Errorf("%w: --version-json must print exactly the fields %s", ErrSelfTest, strings.Join(fields, ", "))
	}
	for _, f := range fields {
		v, ok := doc[f]
		if !ok {
			return fmt.Errorf("%w: --version-json has no %q field", ErrSelfTest, f)
		}
		if _, isString := v.(string); !isString {
			return fmt.Errorf("%w: --version-json field %q is not a string", ErrSelfTest, f)
		}
	}
	if got := doc["app"].(string); got != appID {
		return fmt.Errorf("%w: the binary is app %q, not %q", ErrSelfTest, clip(got), appID)
	}
	if got := doc["version"].(string); strings.TrimPrefix(got, "v") != strings.TrimPrefix(want, "v") {
		return fmt.Errorf("%w: the binary is version %q, not %q", ErrSelfTest, clip(got), want)
	}
	return nil
}

// clip bounds a value read from a binary before it goes into a message.
func clip(s string) string {
	const limit = 64
	if len(s) > limit {
		s = s[:limit] + "..."
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '?'
		}
		return r
	}, s)
}
