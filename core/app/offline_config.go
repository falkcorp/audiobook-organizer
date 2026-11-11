// file: core/app/offline_config.go
// version: 1.0.1
// guid: 7b488a2c-118e-4165-86e5-b2a58dbc34c5
// last-edited: 2026-11-10

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
operator file), and whether it would stop the service from starting. It
exits non-zero when one would, or when a value in the operator layers
does not decode.

It reads only the operator layers: it opens no store and reads no state,
so it runs with the service stopped or while it serves. Flags given to the
service are not seen.`

// configCheckCommand is config check (06 §6.4.5): kernel.CheckConfig over
// the app's operator layers and retired list, opening no store (SEC10-9).
func configCheckCommand(opts Options, mods []module.Module) Command {
	return Command{
		Name:  "config check",
		Short: "List the retired config names set in the environment and the config file",
		Long:  configCheckLong,
		Run: func(ctx context.Context, args []string, stdout io.Writer) error {
			fl := flag.NewFlagSet("config check", flag.ContinueOnError)
			if help, err := parseFlags(fl, args, stdout, "config check", configCheckLong); help || err != nil {
				return err
			}
			uses, err := kernel.CheckConfig(ctx, kernel.ConfigCheck{
				Config: opts.configOptions(), StateDirDefault: opts.StateDir, Modules: mods,
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
			return nil
		},
	}
}

// writeRetiredReport prints one line per retired name set, refused ones
// first, and a summary.
func writeRetiredReport(w io.Writer, uses []config.RetiredUse, list []config.RetiredName) error {
	var b strings.Builder
	if len(uses) == 0 {
		b.WriteString("No retired config names are set.\n")
		_, err := io.WriteString(w, b.String())
		return err
	}
	refused := 0
	for _, pass := range []bool{true, false} {
		for _, u := range uses {
			if u.Refused != pass {
				continue
			}
			kind := "env"
			if u.File {
				kind = "file key"
			}
			verdict := "ignored (a warning at start)"
			if u.Refused {
				verdict = "STOPS THE SERVICE FROM STARTING"
				refused++
			}
			fmt.Fprintf(&b, "%s %s: %s; ", kind, u.Name, verdict)
			switch {
			case u.Canonical == "":
				b.WriteString("the setting was removed")
				if msg := retiredMessage(list, u); msg != "" {
					fmt.Fprintf(&b, " (%s)", msg)
				}
			case strings.HasPrefix(string(u.Canonical), "modules."):
				fmt.Fprintf(&b, "set %s or the operator file key %s instead (a module key: the service judges it when the module's settings are registered)", u.CanonicalEnv, u.Canonical)
			default:
				fmt.Fprintf(&b, "set %s or the operator file key %s instead", u.CanonicalEnv, u.Canonical)
			}
			b.WriteString("\n")
		}
	}
	fmt.Fprintf(&b, "%d retired config names set, %d of them would stop the service from starting.\n", len(uses), refused)
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
