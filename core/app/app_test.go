// file: core/app/app_test.go
// version: 1.0.0
// guid: 2ddf8646-623f-4ba4-ac43-11b166c05e15
// last-edited: 2026-11-07

package app_test

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"testing"

	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/examples/hello"
	"github.com/falkcorp/mediacore/internal/openapi/openapitest"
)

// helloOptions are hello's options with a relative state directory, so any
// write to it would land in the test's working directory.
func helloOptions() app.Options {
	return app.Options{AppID: "hello", EnvPrefix: "HELLO", StateDir: "hello-state", Env: []string{}}
}

// TestDescribeInMemoryNoIO is 06 §6.10's row for the phases that exist:
// Describe over hello touches no path (its working directory stays empty, so
// no state directory or log file was created) and writes nothing to stdout or
// stderr, and two calls return the same documents. The vfs.NewMem storage
// and the derived-index half arrive with storage.
func TestDescribeInMemoryNoIO(t *testing.T) {
	t.Chdir(t.TempDir())
	stdout, stderr := os.Stdout, os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout, os.Stderr = w, w
	d1, err1 := app.Describe(t.Context(), helloOptions(), hello.New())
	d2, err2 := app.Describe(t.Context(), helloOptions(), hello.New())
	os.Stdout, os.Stderr = stdout, stderr
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	written, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if err1 != nil || err2 != nil {
		t.Fatalf("Describe: %v, %v", err1, err2)
	}
	if len(written) > 0 {
		t.Fatalf("Describe wrote to stdout or stderr: %q", written)
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) > 0 {
		t.Fatalf("Describe created %v in the working directory", entries)
	}
	if !bytes.Equal(d1.Core, d2.Core) || !bytes.Equal(d1.OpenAPI["hello"], d2.OpenAPI["hello"]) {
		t.Fatal("two Describe calls returned different documents")
	}
}

// TestOpenAPI_IsValid31 validates the documents Describe builds over hello,
// core's (core/openapi/core.json's content) and hello's, against the OpenAPI
// 3.1 meta-schema.
func TestOpenAPI_IsValid31(t *testing.T) {
	d, err := app.Describe(t.Context(), helloOptions(), hello.New())
	if err != nil {
		t.Fatal(err)
	}
	for owner, b := range map[string][]byte{"core": d.Core, "hello": d.OpenAPI["hello"]} {
		if err := openapitest.Validate(b); err != nil {
			t.Errorf("%s document is not valid OpenAPI 3.1: %v\n%s", owner, err, b)
		}
	}
	var doc struct {
		Info  struct{ Version string }                           `json:"info"`
		Paths map[string]map[string]struct{ OperationID string } `json:"paths"`
	}
	if err := json.Unmarshal(d.OpenAPI["hello"], &doc); err != nil {
		t.Fatal(err)
	}
	op := doc.Paths["/api/hello/v1/greeting"]["get"]
	if doc.Info.Version != "1.0.0" || op.OperationID != "hello_getGreeting" {
		t.Fatalf("hello document: version %q, GET /api/hello/v1/greeting %+v", doc.Info.Version, op)
	}
}

func TestNew_EnvPrefixChecked(t *testing.T) {
	opts := helloOptions()
	opts.EnvPrefix = "hello"
	if _, err := app.New(t.Context(), opts, hello.New()); err == nil {
		t.Fatal("New with a lower-case EnvPrefix succeeded")
	}
}
