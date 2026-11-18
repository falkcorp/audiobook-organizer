// file: core/app/offline_updater_test.go
// version: 1.0.0
// guid: d6e8f0a2-3c5b-4d79-9e14-2a3b4c5d6e7f
// last-edited: 2026-11-18

//go:build unix

package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/updater"
)

// feed is a release source that serves Gotham's releases from memory.
type feed struct {
	bodies map[string][]byte // tag -> the platform asset's body
	asked  []string          // tags looked up
}

func (f *feed) assetName() string { return fmt.Sprintf("batwing-%s-%s", runtime.GOOS, runtime.GOARCH) }

func (f *feed) Latest(context.Context, updater.Channel) (updater.Release, error) {
	return updater.Release{}, errors.New("the fetch must never ask for the latest release")
}

func (f *feed) Release(_ context.Context, tag string) (updater.Release, error) {
	f.asked = append(f.asked, tag)
	if _, ok := f.bodies[tag]; !ok {
		return updater.Release{}, updater.ErrReleaseNotFound
	}
	return updater.Release{Version: tag, Assets: []updater.Asset{{Name: f.assetName(), URL: "mem://" + tag}}}, nil
}

func (f *feed) Open(_ context.Context, a updater.Asset) (io.ReadCloser, int64, error) {
	body, ok := f.bodies[strings.TrimPrefix(a.URL, "mem://")]
	if !ok {
		return nil, -1, errors.New("no such asset")
	}
	return io.NopCloser(bytes.NewReader(body)), int64(len(body)), nil
}

func versionScript(app, version string) []byte {
	return fmt.Appendf(nil, "#!/bin/sh\nprintf '%%s\\n' '{\"app\":\"%s\",\"version\":\"%s\",\"commit\":\"\",\"ui_build_id\":\"dev\",\"binary_id\":\"b1\"}'\n", app, version)
}

func fetchOpts(f *feed) Options {
	return Options{AppID: "batcave", Version: "v0.223.0", Updater: &updater.Options{
		AppID: "batcave", BinaryName: "batwing", Source: f, AssetName: func(_ updater.Release, goos, goarch string) string {
			return fmt.Sprintf("batwing-%s-%s", goos, goarch)
		},
	}}
}

func runFetch(t *testing.T, opts Options, args ...string) (string, error) {
	t.Helper()
	for _, c := range OfflineCommands(opts) {
		if c.Name == "updater fetch-release" {
			var out bytes.Buffer
			err := c.Run(t.Context(), args, &out)
			return out.String(), err
		}
	}
	t.Fatal("OfflineCommands lists no updater fetch-release")
	return "", nil
}

// TestFetchReleaseCommand: the command is listed with its help even for an
// app that configures no source; it writes the named release's binary and
// nothing else, and every failure leaves the destination (and its .part)
// absent and the command non-zero.
func TestFetchReleaseCommand(t *testing.T) {
	t.Run("listed with help that names --version and --out, even with no source", func(t *testing.T) {
		var found *Command
		for _, c := range OfflineCommands(Options{AppID: "batcave"}) {
			if c.Name == "updater fetch-release" {
				found = &c
			}
		}
		if found == nil || found.Short == "" || !strings.Contains(found.Long, "installs nothing") {
			t.Fatalf("command = %+v, want updater fetch-release with its help", found)
		}
		out, err := runFetch(t, Options{AppID: "batcave"}, "--help")
		if err != nil || !strings.Contains(out, "--version") || !strings.Contains(out, "--out") || !strings.Contains(out, "never latest") {
			t.Errorf("help = %q (%v), want usage naming --version and --out", out, err)
		}
	})

	t.Run("a named release is written, mode 0755, through no leftover", func(t *testing.T) {
		f := &feed{bodies: map[string][]byte{"v0.200.0": versionScript("batcave", "0.200.0"), "v0.999.0": versionScript("batcave", "0.999.0")}}
		dest := filepath.Join(t.TempDir(), "batwing.fetched")
		out, err := runFetch(t, fetchOpts(f), "--version", "v0.200.0", "--out", dest)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "v0.200.0") || !strings.Contains(out, dest) {
			t.Errorf("report = %q", out)
		}
		got, err := os.ReadFile(dest)
		if err != nil || string(got) != string(versionScript("batcave", "0.200.0")) {
			t.Errorf("dest = %q (%v), want the named release's bytes", got, err)
		}
		if st, _ := os.Stat(dest); st == nil || st.Mode().Perm() != 0o755 {
			t.Errorf("dest mode = %v, want 0755", st)
		}
		if _, err := os.Stat(dest + ".part"); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s.part left behind (%v)", filepath.Base(dest), err)
		}
		if len(f.asked) != 1 || f.asked[0] != "v0.200.0" {
			t.Errorf("source asked for %v, want only the named tag", f.asked)
		}
	})

	t.Run("refusals and failures leave nothing and exit non-zero", func(t *testing.T) {
		f := &feed{bodies: map[string][]byte{
			"v0.200.0": versionScript("batcave", "0.200.0"),
			"v0.201.0": versionScript("riddler", "0.201.0"), // another app's binary
			"v0.202.0": versionScript("batcave", "0.999.0"), // another version's
		}}
		for _, c := range []struct {
			name string
			opts Options
			args []string
			want string
		}{
			{"no version", fetchOpts(f), []string{"--out", "x"}, "--version is required"},
			{"no out", fetchOpts(f), []string{"--version", "v0.200.0"}, "--out is required"},
			{"stray argument", fetchOpts(f), []string{"--version", "v0.200.0", "--out", "x", "now"}, "unexpected arguments"},
			{"unknown flag", fetchOpts(f), []string{"--url", "https://example.invalid/b"}, "flag provided but not defined"},
			{"latest", fetchOpts(f), []string{"--version", "latest", "--out", "x"}, updater.ErrInvalidVersion.Error()},
			{"a path", fetchOpts(f), []string{"--version", "../../etc/passwd", "--out", "x"}, updater.ErrInvalidVersion.Error()},
			{"a tag the source lacks", fetchOpts(f), []string{"--version", "v0.1.0", "--out", "x"}, updater.ErrReleaseNotFound.Error()},
			{"another app's binary", fetchOpts(f), []string{"--version", "v0.201.0", "--out", "x"}, updater.ErrSelfTest.Error()},
			{"another version's binary", fetchOpts(f), []string{"--version", "v0.202.0", "--out", "x"}, updater.ErrSelfTest.Error()},
			{"no source configured", Options{AppID: "batcave"}, []string{"--version", "v0.200.0", "--out", "x"}, "no release source"},
		} {
			dir := t.TempDir()
			args := slicesReplace(c.args, "x", filepath.Join(dir, "fetched"))
			_, err := runFetch(t, c.opts, args...)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("%s: err = %v, want one containing %q", c.name, err, c.want)
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 0 {
				t.Errorf("%s: left %d files in the destination directory", c.name, len(entries))
			}
		}
	})
}

// slicesReplace is args with every element equal to from replaced by to.
func slicesReplace(args []string, from, to string) []string {
	out := append([]string(nil), args...)
	for i, a := range out {
		if a == from {
			out[i] = to
		}
	}
	return out
}
