// file: internal/maintenance/jobs/generate_itl_tests.go
// version: 1.3.0
// guid: b7e3f1a2-4c5d-6e7f-8a9b-0c1d2e3f4a5b
// last-edited: 2026-07-07

package jobs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/util"

	"log/slog")

func init() { maintenance.Register(&generateITLTestsJob{}) }

type generateITLTestsJob struct{}

func (j *generateITLTestsJob) ID() string       { return "generate-itl-tests" }
func (j *generateITLTestsJob) Name() string     { return "Generate ITL Tests" }
func (j *generateITLTestsJob) Category() string { return "Dev" }
func (j *generateITLTestsJob) Description() string {
	return "Generates a suite of .itl test files for iTunes parser testing"
}
func (j *generateITLTestsJob) DefaultParams() any { return nil }
func (j *generateITLTestsJob) CanResume() bool    { return false }

func (j *generateITLTestsJob) Run(ctx context.Context, store database.Store, reporter maintenance.ProgressReporter, dryRun bool) error {
	if config.AppConfig.RootDir == "" {
		return fmt.Errorf("root_dir is not configured")
	}
	outputDir, err := util.SafeJoin(config.AppConfig.RootDir, ".itunes-writeback", "tests")
	if err != nil {
		return fmt.Errorf("unsafe output dir path: %w", err)
	}

	allBooks, err := store.GetAllBooksCore(100000, 0)
	if err != nil {
		return fmt.Errorf("GetAllBooksCore: %w", err)
	}

	var allBookFiles []database.BookFile
	for _, b := range allBooks {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		files, _ := store.GetBookFiles(b.ID)
		allBookFiles = append(allBookFiles, files...)
	}

	msg := fmt.Sprintf("found %d books and %d book_files", len(allBooks), len(allBookFiles))
	slog.Info(msg)

	if dryRun {
		dry := fmt.Sprintf("dry-run: would generate ITL test suite in %s", outputDir)
		slog.Info(dry)
		return nil
	}

	outputDir = filepath.Clean(outputDir)
	if err := os.RemoveAll(outputDir); err != nil {
		return fmt.Errorf("failed to clean output dir: %w", err)
	}

	if err := itunes.GenerateTestITLSuite(outputDir, allBooks, allBookFiles); err != nil {
		return fmt.Errorf("failed to generate test suite: %w", err)
	}

	done := fmt.Sprintf("Generated ITL test suite in %s with %d books and %d book_files",
		outputDir, len(allBooks), len(allBookFiles))
	slog.Info(done)
	return nil
}
