// file: internal/books/cutover/kindrename/gen/cmd/main.go
// version: 1.0.0
// guid: 4d8c2e61-9f03-47ab-b5e8-1a7c30d96f52
// last-edited: 2026-11-18

// Command cmd regenerates internal/books/cutover/kindrename/map_gen.go
// (`go generate ./internal/books/cutover/kindrename/`).
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/kindrename/gen"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	root, err := gen.FindRoot(cwd)
	if err != nil {
		return err
	}
	out, err := gen.Generate(root)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, filepath.FromSlash(gen.OutputPath)), out, 0o644)
}
