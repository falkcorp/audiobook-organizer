// file: core/app/offline_users_test.go
// version: 0.1.0
// guid: 91c3e5a7-0d2b-4f68-8a14-6e7b9d0c3f52
// last-edited: 2026-11-21

package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/kernel"
)

// The offline identity commands at the command layer (F4-08 B9b): parsing,
// refusals that happen before any store is opened, and the refusal of an app
// that has not declared the identity boot. What the commands do to a store is
// kernel's offline_users_test.go; the CLI half is cmd's.

func runOfflineCmd(ctx context.Context, c Command, args ...string) (string, error) {
	var out bytes.Buffer
	err := c.Run(ctx, args, &out)
	return out.String(), err
}

func declaredIdentityOpts(t *testing.T) Options {
	opts := offlineOpts(t)
	opts.IdentityBoot = true
	return opts
}

func TestResetPasswordCommand_HelpAndArgs(t *testing.T) {
	opts := declaredIdentityOpts(t)
	cmd := command(t, OfflineCommands(opts, offlineKeeper()), "users reset-password")
	out, err := runOfflineCmd(t.Context(), cmd, "--help")
	if err != nil || !strings.Contains(out, "Usage: users reset-password <username>") || !strings.Contains(out, "service stopped") ||
		!strings.Contains(out, "prints the password once") {
		t.Fatalf("--help = %v, output %q", err, out)
	}
	for name, args := range map[string][]string{
		"no username":   nil,
		"blank":         {"  "},
		"unknown flag":  {"bruce", "--batarang"},
		"second arg":    {"bruce", "selina"},
		"flag first":    {"--confirm", "bruce"},
		"password flag": {"bruce", "--password", "hunter2"},
	} {
		if out, err := runOfflineCmd(t.Context(), cmd, args...); err == nil {
			t.Errorf("%s: accepted (%q)", name, out)
		}
	}
}

// TestResetPasswordCommand_NotDeclaredRefuses: an app without the identity boot
// (ABO until B10) gets a clear refusal and the store is not opened.
func TestResetPasswordCommand_NotDeclaredRefuses(t *testing.T) {
	opts := offlineOpts(t)
	keeper := offlineKeeper()
	served(t, opts, keeper)
	before := storeFiles(t, opts)
	cmd := command(t, OfflineCommands(opts, keeper), "users reset-password")
	_, err := runOfflineCmd(t.Context(), cmd, "bruce")
	if !errors.Is(err, kernel.ErrIdentityNotDeclared) {
		t.Fatalf("err = %v, want kernel.ErrIdentityNotDeclared", err)
	}
	if after := storeFiles(t, opts); strings.Join(after, "|") != strings.Join(before, "|") {
		t.Errorf("a refused command changed the store files:\n%v\n%v", before, after)
	}
}

func TestJournalQueueCommand_HelpAndArgs(t *testing.T) {
	opts := declaredIdentityOpts(t)
	cmd := command(t, OfflineCommands(opts, offlineKeeper()), "storage journal-queue")
	for _, h := range []string{"--help", "help", "-h"} {
		out, err := runOfflineCmd(t.Context(), cmd, h)
		if err != nil || !strings.Contains(out, "Usage: storage journal-queue list | clear <seq> --confirm") || !strings.Contains(out, "service stopped") {
			t.Fatalf("%s = %v, output %q", h, err, out)
		}
	}
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"no verb":           {nil, "a verb is required"},
		"unknown verb":      {[]string{"drain"}, "unknown verb"},
		"list with an arg":  {[]string{"list", "7"}, "unexpected arguments"},
		"list unknown flag": {[]string{"list", "--batarang"}, "flag provided but not defined"},
		"clear no seq":      {[]string{"clear", "--confirm"}, "seq is required"},
		"clear bad seq":     {[]string{"clear", "abc", "--confirm"}, "not a seq"},
		"clear seq zero":    {[]string{"clear", "0", "--confirm"}, "not a seq"},
		"clear negative":    {[]string{"clear", "-5", "--confirm"}, "flag provided but not defined"},
	} {
		if _, err := runOfflineCmd(t.Context(), cmd, tc.args...); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to mention %q", name, err, tc.want)
		}
	}
}

// TestJournalQueueClear_RequiresConfirmBeforeAnyOpen: without --confirm the
// command refuses before it resolves a state directory or opens a store, so
// nothing exists afterwards and the refusal names the flag. With the identity
// boot undeclared the same call, with --confirm, is the not-declared refusal,
// which proves --confirm is what the first refusal was about.
func TestJournalQueueClear_RequiresConfirmBeforeAnyOpen(t *testing.T) {
	opts := declaredIdentityOpts(t)
	cmd := command(t, OfflineCommands(opts, offlineKeeper()), "storage journal-queue")
	_, err := runOfflineCmd(t.Context(), cmd, "clear", "7")
	if err == nil || !strings.Contains(err.Error(), "--confirm is required") {
		t.Fatalf("clear without --confirm = %v", err)
	}
	if errors.Is(err, kernel.ErrIdentityNotDeclared) {
		t.Fatal("the refusal came from the kernel, after an open")
	}
	if _, err := os.Stat(filepath.Join(opts.StateDir, "storage")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a refused clear created the store directory (%v)", err)
	}

	plain := offlineOpts(t)
	keeper := offlineKeeper()
	served(t, plain, keeper)
	pc := command(t, OfflineCommands(plain, keeper), "storage journal-queue")
	if _, err := runOfflineCmd(t.Context(), pc, "clear", "7", "--confirm"); !errors.Is(err, kernel.ErrIdentityNotDeclared) {
		t.Errorf("clear --confirm on an app without identity = %v, want ErrIdentityNotDeclared", err)
	}
	if _, err := runOfflineCmd(t.Context(), pc, "list"); !errors.Is(err, kernel.ErrIdentityNotDeclared) {
		t.Errorf("list on an app without identity = %v, want ErrIdentityNotDeclared", err)
	}
}

// TestResetPasswordCommand_DeclaredAppReachesTheKernel: with the identity boot
// declared the command opens the app's served store and gets the kernel's
// refusal of an account that does not exist, wrapped with the command's name,
// and prints nothing (no password for a refused reset).
func TestResetPasswordCommand_DeclaredAppReachesTheKernel(t *testing.T) {
	opts := declaredIdentityOpts(t)
	keeper := offlineKeeper()
	served(t, opts, keeper)
	cmd := command(t, OfflineCommands(opts, keeper), "users reset-password")
	out, err := runOfflineCmd(t.Context(), cmd, "joker")
	if err == nil || !strings.Contains(err.Error(), "users reset-password") || !strings.Contains(err.Error(), `no user named "joker"`) {
		t.Fatalf("err = %v, want the kernel's no-such-user refusal under the command's name", err)
	}
	if out != "" {
		t.Errorf("a refused reset printed %q", out)
	}
	pq := command(t, OfflineCommands(opts, keeper), "storage journal-queue")
	if out, err := runOfflineCmd(t.Context(), pq, "list"); err != nil || !strings.Contains(out, "queue is empty") {
		t.Errorf("list over an empty queue = %q, %v", out, err)
	}
}

func TestResetPasswordReport(t *testing.T) {
	res := auth.OfflineResetResult{UserID: "u-bruce", Username: "bruce", Password: "Wayne-Manor-0001", ClearedRestriction: true, ClearedResetRequired: true}
	rep := resetPasswordReport(res)
	lines := strings.Split(strings.TrimSuffix(rep, "\n"), "\n")
	if last := lines[len(lines)-1]; last != res.Password {
		t.Errorf("the last line is %q, want the password alone", last)
	}
	if strings.Count(rep, res.Password) != 1 {
		t.Errorf("the password is printed %d times", strings.Count(rep, res.Password))
	}
	for _, want := range []string{"Reset the password of bruce (u-bruce)", "sign-in restriction", "password-reset-required"} {
		if !strings.Contains(rep, want) {
			t.Errorf("report lacks %q:\n%s", want, rep)
		}
	}
	if strings.Contains(rep, "WARNING") {
		t.Error("a clean reset warned")
	}
	res.JournalGap = true
	if !strings.Contains(resetPasswordReport(res), "WARNING: the revocation journal") {
		t.Error("a gap was not reported")
	}
	res.ClearedRestriction, res.ClearedResetRequired = false, false
	if rep := resetPasswordReport(res); strings.Contains(rep, "Cleared its") {
		t.Errorf("a reset that cleared nothing says it did:\n%s", rep)
	}
}
