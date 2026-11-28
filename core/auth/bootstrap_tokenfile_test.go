// file: core/auth/bootstrap_tokenfile_test.go
// version: 0.1.0
// guid: 4f7c2e91-6b03-4d58-a9e4-12d8b5c07a36
// last-edited: 2026-11-21

package auth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/falkcorp/mediacore/auth"
)

// Batman names only. The bootstrap token's file name is the app's to set
// (Options.BootstrapTokenFile, RestoreDeps.BootstrapTokenFile): every write,
// removal and redeem goes through one name.

const dottedTokenFile = ".bootstrap-token"

// serviceWithOptions is serviceWith under opts.
func (f *fx) serviceWithOptions(opts auth.Options, files auth.StateFiles) (*auth.Service, error) {
	f.t.Helper()
	return auth.New(context.Background(), opts, auth.Deps{
		Store: f.st, NewBatch: f.st.NewBatch, RBAC: f.rb, Audit: f.audit, Revocations: f.jrnl, Clock: f.clock,
		Hasher: f.hasher, Settings: f.liveSettings, StateFiles: files,
	})
}

// TestBootstrapTokenFile_ConfiguredNameIsTheOneWrittenAndRemoved: with a name
// set, BootWrites writes the token under it and nowhere else, the disabled-
// bootstrap retirement removes that name, and a redeem removes it too: no site
// still names core's default.
func TestBootstrapTokenFile_ConfiguredNameIsTheOneWrittenAndRemoved(t *testing.T) {
	t.Run("mint writes the configured name only", func(t *testing.T) {
		f, files := bootFx(t)
		d := f.restoreDeps(files, nil)
		d.BootstrapTokenFile = dottedTokenFile
		if err := auth.BootWrites(context.Background(), d); err != nil {
			t.Fatal(err)
		}
		tok := files.file(auth.BootstrapTokenLabel, dottedTokenFile)
		if tok == "" {
			t.Fatalf("no token under %q; written: %v", dottedTokenFile, files.written)
		}
		if !f.tokenStored(tok) {
			t.Error("the token written under the configured name is not stored")
		}
		if stray := files.file(auth.BootstrapTokenLabel, auth.BootstrapTokenFile); stray != "" {
			t.Errorf("a token was also written under the default name %q", auth.BootstrapTokenFile)
		}
	})
	t.Run("retirement removes the configured name only", func(t *testing.T) {
		f, files := bootFx(t)
		f.setSettings(auth.Settings{BootstrapEnabled: new(false)})
		d := f.restoreDeps(files, nil)
		d.BootstrapTokenFile = dottedTokenFile
		if err := auth.BootWrites(context.Background(), d); err != nil {
			t.Fatal(err)
		}
		files.mu.Lock()
		defer files.mu.Unlock()
		if want := []string{auth.BootstrapTokenLabel + "/" + dottedTokenFile}; len(files.removed) != 1 || files.removed[0] != want[0] {
			t.Errorf("removals = %v, want %v", files.removed, want)
		}
	})
	t.Run("a redeem removes the configured name only", func(t *testing.T) {
		f, files := bootFx(t)
		svc, err := f.serviceWithOptions(auth.Options{BootstrapTokenFile: dottedTokenFile}, files)
		if err != nil {
			t.Fatal(err)
		}
		f.svc = svc
		tok := f.bootToken(auth.DefaultBootstrapTokenTTL)
		if _, err := svc.RedeemBootstrap(context.Background(), tok, from("192.0.2.70")); err != nil {
			t.Fatal(err)
		}
		files.mu.Lock()
		defer files.mu.Unlock()
		if want := auth.BootstrapTokenLabel + "/" + dottedTokenFile; len(files.removed) != 1 || files.removed[0] != want {
			t.Errorf("removals after a redeem = %v, want [%s]", files.removed, want)
		}
	})
	t.Run("unset keeps core's name", func(t *testing.T) {
		f, files := bootFx(t)
		f.mustBootWrites(files)
		if files.file(auth.BootstrapTokenLabel, auth.BootstrapTokenFile) == "" {
			t.Errorf("no token under the default name; written: %v", files.written)
		}
		if len(files.written) != 1 {
			t.Errorf("BootWrites with the default name wrote %v, want only the token", files.written)
		}
	})
}

// TestBootstrapTokenFile_BadNameFailsClosed: a name that is not a plain file
// name directly under the state directory, or that is the read-only key's file,
// stops auth.New and BootWrites with ErrBadInput, and BootWrites writes and
// removes nothing: left to the file write it would boot with no token file and
// one warning.
func TestBootstrapTokenFile_BadNameFailsClosed(t *testing.T) {
	bad := []string{"..", ".", "../bootstrap-token", "sub/bootstrap-token", "/etc/bootstrap-token", `sub\bootstrap-token`, "sub/", auth.ReadOnlyKeyFile}
	for _, name := range bad {
		t.Run(name, func(t *testing.T) {
			f, files := bootFx(t)
			if _, err := f.serviceWithOptions(auth.Options{BootstrapTokenFile: name}, files); !errors.Is(err, auth.ErrBadInput) {
				t.Errorf("auth.New with token file %q = %v, want ErrBadInput", name, err)
			}
			d := f.restoreDeps(files, nil)
			d.BootstrapTokenFile = name
			if err := auth.BootWrites(context.Background(), d); !errors.Is(err, auth.ErrBadInput) {
				t.Errorf("BootWrites with token file %q = %v, want ErrBadInput", name, err)
			}
			if len(files.written) != 0 || len(files.removed) != 0 {
				t.Errorf("a refused name still touched files: written %v removed %v", files.written, files.removed)
			}
			if _, err := f.st.GetUser(context.Background(), auth.SystemUserID); !errors.Is(err, auth.ErrNotFound) {
				t.Errorf("a refused name still seeded the system user: %v", err)
			}
		})
	}
	t.Run("the dotted name is fine", func(t *testing.T) {
		f, files := bootFx(t)
		if _, err := f.serviceWithOptions(auth.Options{BootstrapTokenFile: dottedTokenFile}, files); err != nil {
			t.Errorf("auth.New refused %q: %v", dottedTokenFile, err)
		}
	})
}
