// file: core/examples/hello/cmd/hello/main.go
// version: 1.0.0
// guid: 04a21a08-0f99-4dac-9ee2-0a0f6beac486
// last-edited: 2026-11-07

// Command hello serves the hello example app (06 §6.12.5), or, as
// `hello openapi [--out <file>]`, writes core's OpenAPI document (06 §6.4.5
// Description.Core) to core/openapi/core.json, its one producer (FE-20).
package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/examples/hello"
)

// version is the example's release version. No build path links a value
// (06 §6.4.5: nothing is linked with ldflags), so it is a constant.
const version = "dev"

// coreModule is the module path whose go.mod marks the core module root.
const coreModule = "github.com/falkcorp/mediacore"

func main() {
	// main is the process owner: the one signal.NotifyContext in core (03 R5,
	// COR6-11).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code, err := run(ctx, os.Args[1:], os.Stdout)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, err) // a *module.ValidationError prints every problem
	}
	os.Exit(code)
}

// options are hello's app.Options.
func options(args []string) app.Options {
	return app.Options{
		AppID:       "hello",
		DisplayName: "Hello",
		Version:     version,
		EnvPrefix:   "HELLO",       // the default, strings.ToUpper(AppID), spelled out
		StateDir:    "hello-state", // compiled default; HELLO_CORE_PATHS_STATE_DIR overrides it
		Args:        args,
	}
}

// run is main without the process: it returns the exit code and the error
// main reports on stderr (2 for a usage or boot refusal, 1 for a failed run).
func run(ctx context.Context, args []string, stdout io.Writer) (int, error) {
	if len(args) > 0 && args[0] == "openapi" {
		if err := openapi(ctx, args[1:], stdout); err != nil {
			return 2, fmt.Errorf("hello openapi: %w", err)
		}
		return 0, nil
	}
	// The flag layer arrives with config (F3); until then hello takes no
	// argument, rather than ignore one.
	if len(args) > 0 {
		return 2, fmt.Errorf("hello: unknown argument %q\nusage: hello [openapi [--out <file>]]", args[0])
	}
	a, err := app.New(ctx, options(args), hello.New())
	if err != nil {
		return 2, err
	}
	if err := a.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return 1, err
	}
	return 0, nil
}

// openapi is `hello openapi [--out <file>]`: Description.Core, built in
// memory by app.Describe, written to <file>, by default core/openapi/core.json
// under the core module root found from the working directory, then reports
// the path on stdout. R1-30's generator and F9-01 call this command line.
func openapi(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("openapi", flag.ContinueOnError)
	fs.SetOutput(stdout)
	out := fs.String("out", "", "file to write (default core/openapi/core.json in the core module)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	d, err := app.Describe(ctx, options(nil), hello.New())
	if err != nil {
		return err
	}
	path := *out
	if path == "" {
		wd, err := os.Getwd()
		if err != nil {
			return err
		}
		root, err := coreRoot(wd)
		if err != nil {
			return err
		}
		path = filepath.Join(root, "openapi", "core.json")
	}
	if err := os.WriteFile(path, d.Core, 0o644); err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, "wrote", path)
	return err
}

// coreRoot walks up from dir to the directory whose go.mod declares the core
// module.
func coreRoot(dir string) (string, error) {
	for d := dir; ; d = filepath.Dir(d) {
		if b, err := os.ReadFile(filepath.Join(d, "go.mod")); err == nil && declares(b, coreModule) {
			return d, nil
		}
		if filepath.Dir(d) == d {
			return "", fmt.Errorf("no go.mod declaring module %s above %s; pass --out", coreModule, dir)
		}
	}
}

// declares reports a go.mod whose module line names path.
func declares(gomod []byte, path string) bool {
	sc := bufio.NewScanner(bytes.NewReader(gomod))
	for sc.Scan() {
		if f := strings.Fields(sc.Text()); len(f) == 2 && f[0] == "module" {
			return strings.Trim(f[1], `"`) == path
		}
	}
	return false
}
