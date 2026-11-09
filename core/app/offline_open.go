// file: core/app/offline_open.go
// version: 1.0.0
// guid: 03c6ad3e-f450-45b0-af00-36a0b6576dd6
// last-edited: 2026-11-08

package app

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/falkcorp/mediacore/kernel"
	"github.com/falkcorp/mediacore/module"
)

// OpenOffline is kernel.OpenOffline under the app's identity (06 §6.4.6;
// R7-4, DATA7-11): the guard-aware open an app's offline subcommands use
// instead of opening the store themselves. It refuses a staged or in-flight
// swap, an empty store and a store a serve boot would migrate, and every
// store a serve boot refuses. readOnly opens Pebble read-only and writes
// nothing. Logs go to standard error: standard output is the command's.
func OpenOffline(ctx context.Context, opts Options, readOnly bool, mods ...module.Module) (*Offline, error) {
	c, err := offlineConfig(opts, mods)
	if err != nil {
		return nil, err
	}
	o, err := kernel.OpenOffline(ctx, c, readOnly)
	if err != nil {
		return nil, err
	}
	return &Offline{o: o}, nil
}

// Offline is one offline open. Storage returns a module's handles as P4c
// builds them. Close commits the session's last batch and closes storage.
type Offline struct{ o *kernel.Offline }

// Storage is module id's StorageHandles.
func (o *Offline) Storage(id module.ID) (module.StorageHandles, error) { return o.o.Storage(id) }

// Close closes the store, once.
func (o *Offline) Close() error { return o.o.Close() }

// offlineConfig is the kernel's view of opts for an offline command: New's,
// with the store path and the state directory, the R1 swap seams, and logs
// on standard error instead of the service's stream or file.
func offlineConfig(opts Options, mods []module.Module) (kernel.Config, error) {
	c, err := kernelConfig(opts, mods)
	if err != nil {
		return kernel.Config{}, err
	}
	c.StorageFS = opts.StorageFS
	if c.StoragePath, c.StateDir, err = storagePath(opts, mods); err != nil {
		return kernel.Config{}, err
	}
	if c.StoragePath != "" {
		if c.StorageClaimsShadow, err = claimsShadow(opts); err != nil {
			return kernel.Config{}, err
		}
		c.StorageStateFiles = opts.StorageStateFiles
	}
	if opts.Logger == nil {
		c.LogOutput = os.Stderr
		if c.Log != nil && c.Log.Output == "file" {
			c.Log.Output = "stderr"
		}
	}
	c.R1Values, c.Listen = nil, nil
	c.BeforeSwap, c.ArchiveExtract = opts.R1BeforeSwap, opts.R1ArchiveExtract
	return c, nil
}

// parseFlags parses args into fl as storage prune-checkpoints parses its
// own: the flag package writes nothing, a parse error is returned with the
// command's name, an argument is refused, and -h/--help writes the usage
// line, long and the flag defaults to stdout (help is true, and a failed
// write is the error).
func parseFlags(fl *flag.FlagSet, args []string, stdout io.Writer, usage, long string) (help bool, err error) {
	fl.SetOutput(io.Discard)
	fl.Usage = func() {}
	if err := fl.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			var defaults bytes.Buffer
			fl.SetOutput(&defaults)
			fl.PrintDefaults()
			_, err := fmt.Fprintf(stdout, "Usage: %s\n\n%s\n\nFlags:\n%s", usage, long, defaults.String())
			return true, err
		}
		return false, fmt.Errorf("%s: %w", fl.Name(), err)
	}
	if fl.NArg() > 0 {
		return false, fmt.Errorf("%s: unexpected arguments %q", fl.Name(), fl.Args())
	}
	return false, nil
}

// absPath is p made absolute, "" for "".
func absPath(p string) (string, error) {
	if p == "" {
		return "", nil
	}
	return filepath.Abs(p)
}
