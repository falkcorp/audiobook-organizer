// file: core/updater/readercheck.go
// version: 1.0.0
// guid: 5d2a9c06-f817-4b3e-a4d1-7c80e5b92f34
// last-edited: 2026-11-18

package updater

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/internal/pebble"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// ReaderCheckOptions configure NewReaderCheck.
type ReaderCheckOptions struct {
	// StateDir is the resolved absolute state directory whose restore/ holds
	// the swap slot. Empty makes every check refuse (cannot_determine): a check
	// that could not read the slot must not pass.
	StateDir string
	// FS reads the sidecars and the slot; nil is the real filesystem.
	FS vfs.FS
	// Run runs a binary and reports what it printed and its exit status; nil
	// runs it for real with the probe timeout and an empty environment but
	// PATH. Tests substitute a fake.
	Run func(ctx context.Context, exe string, args ...string) (stdout, stderr string, code int, err error)
}

// NewReaderCheck is the in-process port of `make rollback`'s guard decision
// (scripts/storage_format_guard.py decide_rollback; 05-storage 5S.3.7, "The
// binary-swap guard"): no sudo, no store opened. For the store at dbPath and
// the binary exe it reads, in order,
//
//  1. the swap slot under StateDir: an in-flight swap refuses with
//     restore_inflight, a staged one with restore_pending (the script lets a
//     pending intent pass, because the older build rejects it at its own boot;
//     the updater does not, 5U.6);
//  2. the levels exe serves: `exe --print-storage-levels`, or for a binary that
//     predates the flag `exe --print-storage-format` (format 1 serves every
//     owner at 0; any other answer is not guessed);
//  3. the store's levels: <db>.storage-levels. Absent, with a checkpoint
//     pointer beside the store, is sidecar_unreadable (a core build opened the
//     store, so a missing file is not "a store from before core"); absent with
//     no pointer falls back to HEAD's projection sidecar <db>.storage-format
//     against the format exe reads.
//
// It refuses level_newer when any store level is above what exe serves,
// format_newer for the legacy form of that, sidecar_unreadable and
// cannot_determine when a file or a probe cannot answer. There is no override
// and no exception. The errors are *Refusal.
func NewReaderCheck(o ReaderCheckOptions) ReaderCheck {
	fs := o.FS
	if fs == nil {
		fs = vfs.Default
	}
	run := o.Run
	if run == nil {
		run = func(ctx context.Context, exe string, args ...string) (string, string, int, error) {
			r, err := runBinary(ctx, exe, args...)
			return r.Stdout, r.Stderr, r.Code, err
		}
	}
	c := readerChecker{stateDir: o.StateDir, fs: fs, run: run}
	return c.check
}

type readerChecker struct {
	stateDir string
	fs       vfs.FS
	run      func(ctx context.Context, exe string, args ...string) (stdout, stderr string, code int, err error)
}

func (c readerChecker) check(ctx context.Context, dbPath, exe string) error {
	if err := c.slot(); err != nil {
		return err
	}
	prev, err := c.binaryLevels(ctx, exe)
	if err != nil {
		return err
	}
	store, ok, rerr := pebble.ReadLevels(c.fs, dbPath)
	if rerr != nil {
		return &Refusal{Reason: ReasonSidecarUnreadable, Detail: "the store's levels file does not read (" + clip(rerr.Error()) + "); nothing was swapped"}
	}
	if !ok {
		return c.legacy(ctx, dbPath, exe, prev)
	}
	if up := above(store, prev); len(up) > 0 {
		return &Refusal{Reason: ReasonLevelNewer, Detail: fmt.Sprintf(
			"the store is at levels %s and %s serves %s: %s above what it can read; a binary swap cannot go back past a level step",
			fmtLevels(store), filepathBase(exe), fmtLevels(prev), strings.Join(up, ", "))}
	}
	return nil
}

// slot refuses while the swap slot holds a swap.
func (c readerChecker) slot() error {
	if strings.TrimSpace(c.stateDir) == "" {
		return &Refusal{Reason: ReasonCannotDetermine, Detail: "no state directory to read the swap slot in", Cause: ErrNoStateDir}
	}
	st, err := restorejournal.ReadSlot(c.fs, c.stateDir)
	if err != nil {
		return &Refusal{Reason: ReasonCannotDetermine, Detail: "the swap slot cannot be read: " + clip(err.Error())}
	}
	if !st.Busy() {
		return nil
	}
	reason := ReasonRestorePending
	if len(st.InFlight) > 0 {
		reason = ReasonRestoreInflight
	}
	cause := st.Err()
	return &Refusal{Reason: reason, Detail: cause.Error(), Cause: cause}
}

func oldLevelsFlagRE() *regexp.Regexp {
	return regexp.MustCompile(`unknown flag: --print-storage-levels(\s|$)`)
}

func oldFormatFlagRE() *regexp.Regexp {
	return regexp.MustCompile(`unknown flag: --print-storage-format(\s|$)`)
}

// binaryLevels returns the levels exe serves.
func (c readerChecker) binaryLevels(ctx context.Context, exe string) (map[string]int, error) {
	out, errOut, code, err := c.run(ctx, exe, "--print-storage-levels")
	if err != nil {
		return nil, &Refusal{Reason: ReasonCannotDetermine, Detail: fmt.Sprintf("cannot run `%s --print-storage-levels`: %s", filepathBase(exe), clip(err.Error()))}
	}
	if code == 0 {
		v, ok := parseVector(out)
		if !ok {
			return nil, &Refusal{Reason: ReasonCannotDetermine, Detail: fmt.Sprintf("`%s --print-storage-levels` printed no JSON level vector", filepathBase(exe))}
		}
		return v, nil
	}
	if !oldLevelsFlagRE().MatchString(errOut) {
		return nil, &Refusal{Reason: ReasonCannotDetermine, Detail: fmt.Sprintf("`%s --print-storage-levels` failed (exit %d)", filepathBase(exe), code)}
	}
	// A binary from before the levels flag: HEAD's format.
	fout, ferr, fcode, err := c.run(ctx, exe, "--print-storage-format")
	if err != nil {
		return nil, &Refusal{Reason: ReasonCannotDetermine, Detail: fmt.Sprintf("%s predates --print-storage-levels and `--print-storage-format` could not run: %s", filepathBase(exe), clip(err.Error()))}
	}
	n, isInt := positiveInt(fout)
	switch {
	case fcode == 0 && isInt && n == 1, fcode != 0 && oldFormatFlagRE().MatchString(ferr):
		// It reads storage format 1, so every owner is at 0.
		return map[string]int{}, nil
	case fcode == 0 && isInt:
		return nil, &Refusal{Reason: ReasonCannotDetermine, Detail: fmt.Sprintf("%s predates --print-storage-levels and reads storage format %d, which maps to no level vector", filepathBase(exe), n)}
	}
	return nil, &Refusal{Reason: ReasonCannotDetermine, Detail: fmt.Sprintf("%s predates --print-storage-levels and `--print-storage-format` failed too", filepathBase(exe))}
}

// legacy is the rule for a store no core build has opened (no levels file):
// HEAD's projection sidecar against the format exe reads.
func (c readerChecker) legacy(ctx context.Context, dbPath, exe string, prev map[string]int) error {
	if _, has, err := pebble.ReadFile(c.fs, dbPath+pebble.CheckpointSuffix); err != nil || has {
		detail := "the store's levels file does not exist, but the store has a checkpoint pointer, so a core build opened and upgraded it: the file is missing, not a store from before core"
		if err != nil {
			detail = "the store's levels file does not exist and the checkpoint pointer beside it cannot be read"
		}
		return &Refusal{Reason: ReasonSidecarUnreadable, Detail: detail + "; nothing was swapped"}
	}
	prevFormat := 1
	for _, v := range prev {
		if v != 0 {
			prevFormat = 2
			break
		}
	}
	store, ok, err := pebble.ReadInt(c.fs, dbPath+".storage-format")
	if err != nil || !ok {
		return &Refusal{Reason: ReasonSidecarUnreadable, Detail: "the store has no levels file and its storage format cannot be read; nothing was swapped"}
	}
	if store > prevFormat {
		return &Refusal{Reason: ReasonFormatNewer, Detail: fmt.Sprintf(
			"%s reads storage format %d, the store is at %d; a binary swap cannot go back past a format change", filepathBase(exe), prevFormat, store)}
	}
	return nil
}

// parseVector reads --print-storage-levels' JSON object: owner names to
// non-negative integer levels.
func parseVector(text string) (map[string]int, bool) {
	var doc map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &doc); err != nil || doc == nil {
		return nil, false
	}
	out := make(map[string]int, len(doc))
	for k, v := range doc {
		if !validOwner(k) {
			return nil, false
		}
		f, isNum := v.(float64)
		if !isNum || f < 0 || f != float64(int(f)) {
			return nil, false
		}
		out[k] = int(f)
	}
	return out, true
}

// validOwner is the owner-name grammar a level vector may carry.
func validOwner(s string) bool {
	return len(s) >= 1 && len(s) <= 64 && ownerPattern().MatchString(s)
}

func ownerPattern() *regexp.Regexp { return regexp.MustCompile(`^[a-z][a-z0-9_.-]*$`) }

// positiveInt reads text as one integer >= 1.
func positiveInt(text string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(text))
	return n, err == nil && n >= 1
}

// above lists the owners whose store level exceeds what the binary serves,
// sorted.
func above(store, prev map[string]int) []string {
	var out []string
	for o, l := range store {
		if l > prev[o] {
			out = append(out, o)
		}
	}
	slices.Sort(out)
	return out
}

// fmtLevels renders a level vector for a message.
func fmtLevels(l map[string]int) string {
	if len(l) == 0 {
		return "{} (every owner at 0)"
	}
	owners := make([]string, 0, len(l))
	for o := range l {
		owners = append(owners, o)
	}
	slices.Sort(owners)
	parts := make([]string, len(owners))
	for i, o := range owners {
		parts[i] = fmt.Sprintf("%s: %d", o, l[o])
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// filepathBase is the last element of exe, for a message.
func filepathBase(exe string) string {
	if i := strings.LastIndexAny(exe, `/\`); i >= 0 {
		return clip(exe[i+1:])
	}
	return clip(exe)
}

// IsRefusal reports whether err is a ReaderCheck refusal and returns it.
func IsRefusal(err error) (*Refusal, bool) {
	var r *Refusal
	return r, errors.As(err, &r)
}
