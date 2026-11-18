// file: core/app/offline_updater.go
// version: 1.0.0
// guid: c5d7e9f1-2b4a-4c68-8d03-1f2a3b4c5d6e
// last-edited: 2026-11-18

package app

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/updater"
)

// fetchReleaseLong is updater fetch-release's full help.
const fetchReleaseLong = `Downloads the one release named by --version from the app's release source
into the file --out and installs nothing: it never touches the running
binary or its .prev slot, restarts nothing and opens no store. It is the
fetch behind make rollback-restore RELEASE=<tag>, which puts a release from
before a storage level step back as .prev.

--version is a release tag of the configured source, such as v1.2.3; latest,
a URL and a path are refused. The release's binary for this platform is
streamed into <out>.part, capped at 512 MiB, run once with --version-json to
check it is this app at that release, and only then renamed to <out>. A
failed download or check deletes <out>.part and leaves <out> absent, and the
command exits non-zero.

It reads no store, so it runs with the service stopped or while it serves.
Trust is HTTPS to the configured release source and no more: no checksum or
signature is checked in this build.`

// fetchReleaseCommand is updater fetch-release (05-updater §5U.4, "Fetching a
// named release"; 06 §6.4.5): updater.Updater.FetchRelease over the app's
// Options.Updater.
func fetchReleaseCommand(opts Options, _ []module.Module) Command {
	return Command{
		Name:  "updater fetch-release",
		Short: "Download a named release's binary to a file, installing nothing",
		Long:  fetchReleaseLong,
		Run: func(ctx context.Context, args []string, stdout io.Writer) error {
			fs := flag.NewFlagSet("updater fetch-release", flag.ContinueOnError)
			// The flag package's own usage and error lines are not written: a
			// parse error is returned, and help is written below.
			fs.SetOutput(io.Discard)
			fs.Usage = func() {}
			version := fs.String("version", "", "the release tag to fetch, e.g. v1.2.3 (required; never latest)")
			out := fs.String("out", "", "the file to write the release's binary to (required)")
			if err := fs.Parse(args); err != nil {
				if errors.Is(err, flag.ErrHelp) {
					var defaults bytes.Buffer
					fs.SetOutput(&defaults)
					fs.PrintDefaults()
					_, err := fmt.Fprintf(stdout, "Usage: updater fetch-release --version <tag> --out <path>\n\n%s\n\nFlags:\n%s", fetchReleaseLong, defaults.String())
					return err
				}
				return fmt.Errorf("updater fetch-release: %w", err)
			}
			switch {
			case fs.NArg() > 0:
				return fmt.Errorf("updater fetch-release: unexpected arguments %q", fs.Args())
			case strings.TrimSpace(*version) == "":
				return errors.New("updater fetch-release: --version is required")
			case strings.TrimSpace(*out) == "":
				return errors.New("updater fetch-release: --out is required")
			}
			return fetchRelease(ctx, opts, *version, *out, stdout)
		},
	}
}

// fetchRelease builds the updater over the app's source and fetches version.
func fetchRelease(ctx context.Context, opts Options, version, out string, stdout io.Writer) error {
	if opts.Updater == nil {
		return errors.New("updater fetch-release: this build has no release source configured")
	}
	// The fetch opens no store and runs nothing as the service, so the running
	// version only names the client in the source's requests.
	running := opts.Version
	if strings.TrimSpace(running) == "" {
		running = "dev"
	}
	u, err := updater.New(*opts.Updater, updater.Deps{Log: opts.Logger}, updater.WithVersion(running))
	if err != nil {
		return fmt.Errorf("updater fetch-release: %w", err)
	}
	if err := u.FetchRelease(ctx, version, out); err != nil {
		return fmt.Errorf("updater fetch-release: %w", err)
	}
	_, err = fmt.Fprintf(stdout, "fetched release %s to %s\n", strings.TrimSpace(version), out)
	return err
}
