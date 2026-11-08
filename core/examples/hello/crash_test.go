// file: core/examples/hello/crash_test.go
// version: 1.0.0
// guid: d2573b3f-2985-4783-baa2-5b0ee3888984
// last-edited: 2026-11-08

package hello_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/coretest/crashmatrix"
	"github.com/falkcorp/mediacore/examples/hello"
)

// bootHello is the crash matrix's boot of hello.
func bootHello(_ testing.TB, inj *crashmatrix.Injector) error {
	_, err := inj.New(context.Background(), app.Options{AppID: "hello", Logger: slog.New(slog.DiscardHandler)}, hello.New())
	return err
}

func TestBootCrashMatrix(t *testing.T) {
	fresh := crashmatrix.Fixture{Name: "fresh", StateDir: t.TempDir()}
	crashmatrix.Run(t, crashmatrix.Scenario{Build: bootHello, Fixtures: []crashmatrix.Fixture{fresh}})
}
