// file: core/app/offline_users.go
// version: 0.1.0
// guid: 3f9a1c62-7e0b-4d85-b6a4-1c8e5d2f0a97
// last-edited: 2026-11-21

package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/kernel"
	"github.com/falkcorp/mediacore/module"
)

// users reset-password (05-authn §4.14 "The offline command"; F4-08 B9b): the
// operator's way back into an account whose sign-in a restore restricted (a
// last admin the restore had to keep), or that has lost every other way in.
// Everything it decides is kernel.ResetPasswordOffline's and
// auth.ResetPasswordOffline's; this file parses the line and prints the result.
//
// The new password is generated and printed once, to standard output, and is
// stored nowhere. The command takes no password argument or environment
// variable: an argument lands in the shell history and the process table, an
// environment variable in the process's environment, and a prompt needs a
// terminal the wrapper (Command.Run has no stdin) does not give it. The
// operator reads it from the terminal, or redirects stdout to a file of their
// own making, and should change it at once through Account -> Change password.

const resetPasswordName = "users reset-password"

const resetPasswordUsage = "users reset-password <username>"

const resetPasswordLong = `Resets one account's password, with the service stopped.

  users reset-password <username>

The service must not be running: the command opens the store the way every
offline command does, and refuses a store another process has open, a staged
or in-flight restore or reset, a store that still has migrations to run, and
a store whose state directory is not yours.

It generates a new password, stores its hash, and prints the password once to
standard output. The password is not stored or logged anywhere else; if you
lose it, run the command again. Change it from the Account page after you
sign in.

The reset clears the account's sign-in restriction and its "password reset
required" flag, so every way of signing in works again, and bumps its
credential epoch, so every session and every pending link of the account is
dead. It is journaled in the revocation journal and audited
(core.user.reset_password, method offline).

It changes nothing else: the account's roles and status are untouched (a
disabled or pending account is refused, because a password does not turn an
account on), its API keys keep the status a restore gave them (an admin
re-enables them from the API keys page), and an external identity a restore
unlinked stays unlinked (the user links it again by signing in through it).

An app that has not declared the core identity (it keeps its own accounts)
refuses the command.`

// resetPasswordCommand is users reset-password.
func resetPasswordCommand(opts Options, mods []module.Module) Command {
	return Command{
		Name:  resetPasswordName,
		Short: "Reset one account's password and clear its sign-in restriction, with the service stopped",
		Long:  resetPasswordLong,
		Run: func(ctx context.Context, args []string, stdout io.Writer) error {
			name, flagArgs := splitLeadingArg(args)
			fl := flag.NewFlagSet(resetPasswordName, flag.ContinueOnError)
			if help, err := parseFlags(fl, flagArgs, stdout, resetPasswordUsage, resetPasswordLong); help || err != nil {
				return err
			}
			if strings.TrimSpace(name) == "" {
				return fmt.Errorf("%s: the account's username is required (usage: %s)", resetPasswordName, resetPasswordUsage)
			}
			c, err := offlineConfig(ctx, opts, mods, readWriteOwner(false))
			if err != nil {
				return err
			}
			res, err := kernel.ResetPasswordOffline(ctx, c, name)
			if err != nil {
				return fmt.Errorf("%s: %w", resetPasswordName, err)
			}
			_, err = io.WriteString(stdout, resetPasswordReport(res))
			return err
		},
	}
}

// resetPasswordReport is what the command prints: what was cleared, then the
// password as the last line with nothing after it, so a redirect of stdout
// leaves one clean line to cut. It is the only place the password is written.
func resetPasswordReport(res auth.OfflineResetResult) string {
	var out strings.Builder
	fmt.Fprintf(&out, "Reset the password of %s (%s).\n", res.Username, res.UserID)
	if res.ClearedRestriction {
		out.WriteString("Cleared its sign-in restriction.\n")
	}
	if res.ClearedResetRequired {
		out.WriteString("Cleared its password-reset-required flag.\n")
	}
	if res.JournalGap {
		out.WriteString("WARNING: the revocation journal could not take the lines for this reset, so a gap was recorded in its place. Check that core.backup.dir is writable before the next start.\n")
	}
	out.WriteString("New password (shown once, stored nowhere; change it after you sign in):\n")
	out.WriteString(res.Password)
	out.WriteString("\n")
	return out.String()
}

// splitLeadingArg separates a leading positional argument (one not starting
// with "-") from the flags after it, which is the order the offline commands
// take: the wrapper parses none of the command's flags, and the flag package
// stops at the first argument that is not one.
func splitLeadingArg(args []string) (arg string, flags []string) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return args[0], args[1:]
	}
	return "", args
}

// errNoSeq is the refusal of a missing or unreadable seq.
var errNoSeq = errors.New("the entry's seq is required, before the flags")
