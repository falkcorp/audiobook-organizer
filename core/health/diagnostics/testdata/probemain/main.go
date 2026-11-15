// file: core/health/diagnostics/testdata/probemain/main.go
// version: 1.0.0
// guid: 4a9c1e6d-2b57-4f83-8d0a-5e7b3c9f1a24
// last-edited: 2026-11-15

// Command probemain is built and run by TestRedesignRefusal_RealBinary: a
// production binary (not a test binary) that calls diagnostics.New and the
// zero Exporter's Build. While RedesignPendingF8_03b is true both must refuse
// with ErrRedesignPendingF8_03b. It exits 0 and prints "refused" when they do,
// and exits 1 with what it got when they do not.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/falkcorp/mediacore/health/diagnostics"
	"github.com/falkcorp/mediacore/tasks"
)

func main() {
	failed := false

	e, err := diagnostics.New(diagnostics.Config{})
	if e != nil || !errors.Is(err, diagnostics.ErrRedesignPendingF8_03b) {
		fmt.Printf("New = %v, %v; want nil, ErrRedesignPendingF8_03b\n", e, err)
		failed = true
	}

	var zero diagnostics.Exporter
	res, err := zero.Build(context.Background(), "", tasks.Actor{}, diagnostics.ExportParams{}, nil)
	if !errors.Is(err, diagnostics.ErrRedesignPendingF8_03b) {
		fmt.Printf("zero Exporter.Build = %v, %v; want ErrRedesignPendingF8_03b\n", res, err)
		failed = true
	}

	if failed {
		os.Exit(1)
	}
	fmt.Println("refused")
}
