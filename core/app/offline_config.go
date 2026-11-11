// file: core/app/offline_config.go
// version: 1.0.2
// guid: 7b488a2c-118e-4165-86e5-b2a58dbc34c5
// last-edited: 2026-11-11

package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/kernel"
	"github.com/falkcorp/mediacore/module"
)

const configCheckLong = `Lists every retired config name set in the operator layers (the
environment and the operator file) with the name that replaced it, so a
unit, compose or launchd file can be converted before the release that
retires those names is deployed (05-config §1.3.6).

A retired name is never read. One set for a protected key (an admin,
owner or operator key, or a secret) makes the service refuse to start,
naming it; any other one is ignored with a warning. The command prints
each name set, what it became (its canonical env name and its key in the
operator file), and whether it would stop the service from starting.

Module settings are not registered by this command yet, so a retired name
of a module key is listed as NOT CHECKED: a start with that module enabled
judges it and may refuse. Convert those names too.

It exits non-zero when a name would stop the service from starting, when
one is not checked, or when a value in the operator layers does not
decode. Give the flags the service starts with as --set <path>=<value>
(repeatable, by config path), so their values are decoded as a start
decodes them.

It reads only the operator layers: it opens no store and reads no state,
so it runs with the service stopped or while it serves.`

// setFlags collects --set <path>=<value>, the service's flags by config path.
type setFlags map[config.Path]string

func (f setFlags) String() string { return "" }

func (f setFlags) Set(v string) error {
	p, val, ok := strings.Cut(v, "=")
	if !ok || p == "" {
		return fmt.Errorf("want <path>=<value>, got %q", v)
	}
	if _, dup := f[config.Path(p)]; dup {
		return fmt.Errorf("%s is set twice", p)
	}
	f[config.Path(p)] = val
	return nil
}

// configCheckCommand is config check (06 §6.4.5): kernel.CheckConfig over
// the app's operator layers and retired list, opening no store (SEC10-9).
// Seam for F3-07: kernel.ConfigCheck.ModuleSections is left nil until the
// app hands the enabled modules' config sections to the kernel; F3-07 passes
// them here so module keys are judged as a start judges them.
func configCheckCommand(opts Options, mods []module.Module) Command {
	return Command{
		Name:  "config check",
		Short: "List the retired config names set in the environment and the config file",
		Long:  configCheckLong,
		Run: func(ctx context.Context, args []string, stdout io.Writer) error {
			fl := flag.NewFlagSet("config check", flag.ContinueOnError)
			flags := setFlags{}
			fl.Var(flags, "set", "a flag the service starts with, as `path=value` by config path (repeatable)")
			if help, err := parseFlags(fl, args, stdout, "config check [--set path=value ...]", configCheckLong); help || err != nil {
				return err
			}
			co := opts.configOptions()
			if len(flags) > 0 {
				co.Flags = flags
			}
			uses, err := kernel.CheckConfig(ctx, kernel.ConfigCheck{
				Config: co, StateDirDefault: opts.StateDir, Modules: mods,
			})
			if werr := writeRetiredReport(stdout, uses, opts.RetiredConfigNames); werr != nil {
				return errors.Join(err, werr)
			}
			if rerr, refused := errors.AsType[*config.RetiredNameError](err); refused {
				return fmt.Errorf("config check: %d retired config names would stop the service from starting", len(rerr.Uses))
			}
			if err != nil {
				return fmt.Errorf("config check: %w", err)
			}
			if n := countUnchecked(uses); n > 0 {
				return fmt.Errorf("config check: %d retired config names of module keys are not checked; convert them", n)
			}
			return nil
		},
	}
}

// countUnchecked counts the uses config check could not judge.
func countUnchecked(uses []config.RetiredUse) int {
	n := 0
	for _, u := range uses {
		if u.Unchecked {
			n++
		}
	}
	return n
}

// writeRetiredReport prints one line per retired name set, refused ones
// first, then unchecked ones, then ignored ones, and a summary.
func writeRetiredReport(w io.Writer, uses []config.RetiredUse, list []config.RetiredName) error {
	var b strings.Builder
	if len(uses) == 0 {
		b.WriteString("No retired config names are set.\n")
		_, err := io.WriteString(w, b.String())
		return err
	}
	class := func(u config.RetiredUse) int {
		switch {
		case u.Refused:
			return 0
		case u.Unchecked:
			return 1
		}
		return 2
	}
	refused, unchecked := 0, 0
	for pass := range 3 {
		for _, u := range uses {
			if class(u) != pass {
				continue
			}
			kind := "env"
			if u.File {
				kind = "file key"
			}
			verdict := "ignored (a warning at start)"
			switch {
			case u.Refused:
				verdict = "STOPS THE SERVICE FROM STARTING"
				refused++
			case u.Unchecked:
				verdict = "NOT CHECKED (module settings are not registered by config check yet; a start with the module enabled judges it and may refuse)"
				unchecked++
			}
			fmt.Fprintf(&b, "%s %s: %s; ", kind, u.Name, verdict)
			if u.Canonical == "" {
				b.WriteString("the setting was removed")
				if msg := retiredMessage(list, u); msg != "" {
					fmt.Fprintf(&b, " (%s)", msg)
				}
			} else {
				fmt.Fprintf(&b, "set %s or the operator file key %s instead", u.CanonicalEnv, u.Canonical)
			}
			b.WriteString("\n")
		}
	}
	fmt.Fprintf(&b, "%d retired config names set, %d of them would stop the service from starting, %d not checked.\n", len(uses), refused, unchecked)
	_, err := io.WriteString(w, b.String())
	return err
}

// retiredMessage is the app's reason for a dropped name, "" when it gave
// none.
func retiredMessage(list []config.RetiredName, u config.RetiredUse) string {
	for _, r := range list {
		if r.Name == u.Name && r.File == u.File {
			return r.Message
		}
	}
	return ""
}
