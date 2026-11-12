// file: core/app/offline_cutover_blockers.go
// version: 1.0.0
// guid: c41e7a58-9d2b-4f63-b805-27e1a9d6c3f0
// last-edited: 2026-11-12

package app

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/falkcorp/mediacore/kernel"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
)

// storage cutover-blockers (F4-06; 05-storage §5S.3.6 "Cutover preflight",
// 09 §9.2.3): the operator's way through a cutover the preflight blocked. It
// runs on the cutover build, with the service stopped, over a store the
// cutover has not started on: it lists the rows that block it, repairs one
// (fix) or removes one with the rows that go with it (delete). Every edit is
// one durable batch (the row, a record under c/storage/cutover/blockers/ and
// the core.storage.cutover_blocker_<action> audit event) and is refused
// unless the step's preflight, run over that batch, no longer names the row
// and names nothing new; a refused edit writes nothing. A store whose cutover
// has started is refused, naming make rollback-restore.
//
// A blocking row can hold credential material, so this command never prints a
// row's value: `list` prints keys and the step's reasons; `list --out <file>`
// writes the raw rows to a new file only the operator can read (mode 0600,
// created exclusively), and `fix <key> --from <file>` reads the repaired value
// back from a file of the same format.

const cutoverBlockersMaxFile = 16 << 20

const cutoverBlockersLong = `Lists, repairs or removes the rows that block the cutover.

  storage cutover-blockers list [--out <file>]
  storage cutover-blockers fix <key> --from <file>
  storage cutover-blockers delete <key> --confirm

It runs on the cutover build, with the service stopped, over the store the
pre-freeze release served: it refuses a store the cutover has started on,
and tells you to restore the pre-upgrade archive (make rollback-restore).

list prints each blocking row's key (as keyhex= too, the form fix and delete
take), the step, the family, the reason and the way out, never a value. With
--out it also writes the rows to <file>, a JSON array of
{"key": "<hex>", "value": "<base64>"} objects, to a new file only you can
read (mode 0600; it refuses a file that exists). The values are credential
material in some families: repair them there, then delete the file.

fix replaces the row at <key> with the value of the entry of --from that has
that key (the --out format; edit the value there). delete removes the row and
the rows that go with it. <key> is the key's bytes as printed, or hex:<hex>
(the keyhex= form), and must be one the current list names. Both are refused
unless the step's offered way out includes the action, and unless the step's
preflight, run over the edit, no longer names the row; a refused edit
changes nothing. Each is audited and recorded under c/storage/cutover/
blockers/. delete requires --confirm: the rows are gone afterwards.`

const (
	cutoverBlockersUsage = "storage cutover-blockers list [--out <file>] | fix <key> --from <file> | delete <key> --confirm"
	cutoverBlockersName  = "storage cutover-blockers"
)

// cutoverBlockersCommand is storage cutover-blockers.
func cutoverBlockersCommand(opts Options, mods []module.Module) Command {
	return Command{
		Name:  cutoverBlockersName,
		Short: "List, fix or delete the rows that block the cutover, with the service stopped",
		Long:  cutoverBlockersLong,
		Run: func(ctx context.Context, args []string, stdout io.Writer) error {
			if len(args) == 0 {
				return fmt.Errorf("%s: a verb is required: list, fix or delete (usage: %s)", cutoverBlockersName, cutoverBlockersUsage)
			}
			verb, rest := args[0], args[1:]
			switch verb {
			case "-h", "-help", "--help", "help":
				_, err := fmt.Fprintf(stdout, "Usage: %s\n\n%s\n", cutoverBlockersUsage, cutoverBlockersLong)
				return err
			case "list":
				return runBlockersList(ctx, opts, mods, rest, stdout)
			case "fix":
				return runBlockersFix(ctx, opts, mods, rest, stdout)
			case "delete":
				return runBlockersDelete(ctx, opts, mods, rest, stdout)
			}
			return fmt.Errorf("%s: unknown verb %q: want list, fix or delete", cutoverBlockersName, verb)
		},
	}
}

// openBlockers is kernel.OpenCutoverBlockers over the app's configuration.
func openBlockers(ctx context.Context, opts Options, mods []module.Module, readOnly bool) (*kernel.CutoverBlockers, error) {
	c, err := offlineConfig(opts, mods)
	if err != nil {
		return nil, err
	}
	cb, err := kernel.OpenCutoverBlockers(ctx, c, readOnly, kernel.CutoverBlockersOptions{})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", cutoverBlockersName, err)
	}
	return cb, nil
}

// blockersRow is one entry of the --out / --from file.
type blockersRow struct {
	Key   string `json:"key"`   // hex of the key's bytes
	Value []byte `json:"value"` // base64 in the file
}

func runBlockersList(ctx context.Context, opts Options, mods []module.Module, args []string, stdout io.Writer) (err error) {
	fl := flag.NewFlagSet(cutoverBlockersName+" list", flag.ContinueOnError)
	out := fl.String("out", "", "also write the blocking rows, values included, to this new 0600 file")
	if help, err := parseFlags(fl, args, stdout, "storage cutover-blockers list [--out <file>]", cutoverBlockersLong); help || err != nil {
		return err
	}
	cb, err := openBlockers(ctx, opts, mods, true)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, cb.Close()) }()
	blockers, err := cb.List(ctx)
	if err != nil {
		return fmt.Errorf("%s list: %w", cutoverBlockersName, err)
	}
	if len(blockers) == 0 {
		_, err = io.WriteString(stdout, "No blocking rows: the cutover preflight is clean.\n")
		return err
	}
	// The report's own last line points at the pre-freeze release; this
	// command runs on the cutover build, so only its blocker lines are used.
	lines := storage.CutoverBlockerReport(blockers)
	var b strings.Builder
	for i, bl := range blockers {
		fmt.Fprintf(&b, "%s keyhex=%s\n", lines[i], hex.EncodeToString(bl.Key))
	}
	fmt.Fprintf(&b, "%d blocking row(s). Fix or delete each with its keyhex (hex:<keyhex>), then run the upgrade again.\n", len(blockers))
	if _, err := io.WriteString(stdout, b.String()); err != nil {
		return err
	}
	if strings.TrimSpace(*out) == "" {
		return nil
	}
	rows, err := cb.Rows(ctx, blockers)
	if err != nil {
		return fmt.Errorf("%s list: %w", cutoverBlockersName, err)
	}
	if err := writeBlockerRows(*out, rows); err != nil {
		return fmt.Errorf("%s list: %w", cutoverBlockersName, err)
	}
	_, err = fmt.Fprintf(stdout, "Wrote %d row(s) with their values to %s (mode 0600). They can hold credential hashes: delete the file once the rows are fixed.\n", len(rows), *out)
	return err
}

// writeBlockerRows writes rows to a new file only its owner can read; a
// partly written file is removed.
func writeBlockerRows(path string, rows []kernel.BlockerRow) (err error) {
	entries := make([]blockersRow, len(rows))
	for i, r := range rows {
		entries[i] = blockersRow{Key: hex.EncodeToString(r.Key), Value: r.Value}
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.Write(append(data, '\n')); err == nil {
		err = f.Sync()
	}
	if err = errors.Join(err, f.Close()); err != nil {
		return errors.Join(err, os.Remove(path))
	}
	return nil
}

// splitBlockerKey takes the key, the first argument when it is not a flag,
// off args: flags follow it.
func splitBlockerKey(args []string) (key string, flags []string) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return args[0], args[1:]
	}
	return "", args
}

// parseBlockerKey is a key's bytes: hex:<hex>, or the argument itself.
func parseBlockerKey(arg string) ([]byte, error) {
	if h, ok := strings.CutPrefix(arg, "hex:"); ok {
		k, err := hex.DecodeString(h)
		if err != nil {
			return nil, fmt.Errorf("the key %q is not hex: %w", arg, err)
		}
		if len(k) == 0 {
			return nil, errors.New("the key is empty")
		}
		return k, nil
	}
	return []byte(arg), nil
}

func runBlockersFix(ctx context.Context, opts Options, mods []module.Module, args []string, stdout io.Writer) (err error) {
	keyArg, flagArgs := splitBlockerKey(args)
	fl := flag.NewFlagSet(cutoverBlockersName+" fix", flag.ContinueOnError)
	from := fl.String("from", "", "the file (the list --out format) holding the repaired value (required)")
	if help, err := parseFlags(fl, flagArgs, stdout, "storage cutover-blockers fix <key> --from <file>", cutoverBlockersLong); help || err != nil {
		return err
	}
	if keyArg == "" {
		return fmt.Errorf("%s fix: the row's key is required, before the flags", cutoverBlockersName)
	}
	if strings.TrimSpace(*from) == "" {
		return fmt.Errorf("%s fix: --from is required", cutoverBlockersName)
	}
	key, err := parseBlockerKey(keyArg)
	if err != nil {
		return fmt.Errorf("%s fix: %w", cutoverBlockersName, err)
	}
	value, err := readBlockerValue(*from, key)
	if err != nil {
		return fmt.Errorf("%s fix: %w", cutoverBlockersName, err)
	}
	cb, err := openBlockers(ctx, opts, mods, false)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, cb.Close()) }()
	res, err := cb.Fix(ctx, key, value, operatorName())
	if err != nil {
		return fmt.Errorf("%s fix: %w", cutoverBlockersName, err)
	}
	_, err = fmt.Fprintf(stdout, "Fixed %s (step %s, record %d). Run storage cutover-blockers list to see what is left, and delete %s: it holds the row's value.\n",
		storage.PrintableKey(key), res.Step, res.N, *from)
	return err
}

// readBlockerValue is the value of the one entry of the file at path whose
// key is key. The file is decoded strictly (a JSON array of {key, value}, no
// unknown field, no trailing data) and size-capped; an error names no value.
func readBlockerValue(path string, key []byte) ([]byte, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > cutoverBlockersMaxFile {
		return nil, fmt.Errorf("%s is not a regular file of at most %d bytes", path, cutoverBlockersMaxFile)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) > cutoverBlockersMaxFile {
		return nil, fmt.Errorf("%s grew past %d bytes while it was read", path, cutoverBlockersMaxFile)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var rows []blockersRow
	if err := dec.Decode(&rows); err != nil {
		return nil, fmt.Errorf("%s is not a list --out file: %w", path, err)
	}
	if dec.More() {
		return nil, fmt.Errorf("%s has data after the array", path)
	}
	want := hex.EncodeToString(key)
	var value []byte
	var found int
	for _, r := range rows {
		if strings.EqualFold(r.Key, want) {
			found++
			value = r.Value
		}
	}
	switch found {
	case 0:
		return nil, fmt.Errorf("%s has no entry for the key %s", path, storage.PrintableKey(key))
	case 1:
		return value, nil
	}
	return nil, fmt.Errorf("%s has %d entries for the key %s: exactly one is required", path, found, storage.PrintableKey(key))
}

func runBlockersDelete(ctx context.Context, opts Options, mods []module.Module, args []string, stdout io.Writer) (err error) {
	keyArg, flagArgs := splitBlockerKey(args)
	fl := flag.NewFlagSet(cutoverBlockersName+" delete", flag.ContinueOnError)
	confirm := fl.Bool("confirm", false, "delete the row (required)")
	if help, err := parseFlags(fl, flagArgs, stdout, "storage cutover-blockers delete <key> --confirm", cutoverBlockersLong); help || err != nil {
		return err
	}
	if keyArg == "" {
		return fmt.Errorf("%s delete: the row's key is required, before the flags", cutoverBlockersName)
	}
	if !*confirm {
		return fmt.Errorf("%s delete: --confirm is required; the row and the rows that go with it are deleted (list --out keeps a copy of the row)", cutoverBlockersName)
	}
	key, err := parseBlockerKey(keyArg)
	if err != nil {
		return fmt.Errorf("%s delete: %w", cutoverBlockersName, err)
	}
	cb, err := openBlockers(ctx, opts, mods, false)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, cb.Close()) }()
	res, err := cb.Delete(ctx, key, operatorName())
	if err != nil {
		return fmt.Errorf("%s delete: %w", cutoverBlockersName, err)
	}
	_, err = fmt.Fprintf(stdout, "Deleted %s (step %s, record %d; %d id(s): %s). Run storage cutover-blockers list to see what is left.\n",
		storage.PrintableKey(key), res.Step, res.N, len(res.IDs), strings.Join(res.IDs, ", "))
	return err
}
