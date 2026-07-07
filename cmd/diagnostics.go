// file: cmd/diagnostics.go
// version: 1.2.0
// guid: c8f6a0d4-2a8b-48cf-9d08-02cc9915d9fc
// last-edited: 2026-07-07

package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/cockroachdb/pebble/v2"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/spf13/cobra"
)

var (
	diagnosticsCmd = &cobra.Command{
		Use:   "diagnostics",
		Short: "Debugging and cleanup helpers",
		Long:  "Diagnostic utilities for inspecting and repairing the audiobook database.",
	}

	cleanupCmd = &cobra.Command{
		Use:   "cleanup-invalid",
		Short: "Remove placeholder-based file paths",
		RunE: func(cmd *cobra.Command, args []string) error {
			force, _ := cmd.Flags().GetBool("yes")
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			return runCleanupInvalidBooks(force, dryRun)
		},
	}

	queryCmd = &cobra.Command{
		Use:   "query",
		Short: "Inspect stored book records",
		RunE: func(cmd *cobra.Command, args []string) error {
			limit, _ := cmd.Flags().GetInt("limit")
			prefix, _ := cmd.Flags().GetString("prefix")
			raw, _ := cmd.Flags().GetBool("raw")
			return runDiagnosticsQuery(limit, prefix, raw)
		},
	}
)

func init() {
	cleanupCmd.Flags().Bool("yes", false, "Skip confirmation prompt")
	cleanupCmd.Flags().Bool("dry-run", false, "List invalid records without deleting")

	queryCmd.Flags().Int("limit", 5, "Number of records to display")
	queryCmd.Flags().String("prefix", "book:", "Key prefix to inspect when --raw is set")
	queryCmd.Flags().Bool("raw", false, "Show raw Pebble key/value data (Pebble only)")

	diagnosticsCmd.AddCommand(cleanupCmd)
	diagnosticsCmd.AddCommand(queryCmd)
}

func ensureDiagnosticsStore() (database.Store, func(), error) {
	store, err := database.InitializeStore(
		config.AppConfig.DatabaseType,
		config.AppConfig.DatabasePath,
		config.AppConfig.EnableSQLite,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to initialize database: %w", err)
	}

	cleanup := func() {
		database.CloseStore()
	}
	return store, cleanup, nil
}

func runCleanupInvalidBooks(force, dryRun bool) error {
	store, closer, err := ensureDiagnosticsStore()
	if err != nil {
		return err
	}
	defer closer()

	fmt.Printf("Inspecting books in %s (%s)\n", config.AppConfig.DatabasePath, config.AppConfig.DatabaseType)

	const batchSize = 5000
	offset := 0
	invalid := make([]database.BookCore, 0)
	placeholders := []string{"{series}", "{narrator}", "{author}", "{title}"}

	for {
		books, err := store.GetAllBooksCore(batchSize, offset)
		if err != nil {
			return fmt.Errorf("failed to fetch books: %w", err)
		}
		if len(books) == 0 {
			break
		}
		for _, book := range books {
			if hasPlaceholder(book.FilePath, placeholders) {
				invalid = append(invalid, book)
			}
		}
		offset += len(books)
		if len(books) < batchSize {
			break
		}
	}

	if len(invalid) == 0 {
		fmt.Println("No invalid book records detected.")
		return nil
	}

	fmt.Printf("Found %d invalid records:\n", len(invalid))
	for i, book := range invalid {
		fmt.Printf("%2d. ID: %s\n", i+1, book.ID)
		fmt.Printf("    Title: %s\n", book.Title)
		fmt.Printf("    Path:  %s\n", book.FilePath)
	}

	if dryRun {
		fmt.Println("Dry run enabled; no deletions were performed.")
		return nil
	}

	if !force {
		confirmed, err := promptYesNo(fmt.Sprintf("Delete %d records", len(invalid)))
		if err != nil {
			return err
		}
		if !confirmed {
			fmt.Println("Aborted. No records deleted.")
			return nil
		}
	}

	deleted := 0
	for _, book := range invalid {
		if err := store.DeleteBook(book.ID); err != nil {
			fmt.Printf("Failed to delete %s: %v\n", book.ID, err)
			continue
		}
		deleted++
	}

	fmt.Printf("Deleted %d invalid records. Run a rescan to repopulate clean entries.\n", deleted)
	return nil
}

func runDiagnosticsQuery(limit int, prefix string, raw bool) error {
	if limit <= 0 {
		return errors.New("limit must be positive")
	}

	if raw {
		if config.AppConfig.DatabaseType != "pebble" {
			return fmt.Errorf("raw inspection is only available for Pebble databases")
		}
		return runRawPebbleQuery(limit, prefix)
	}

	store, closer, err := ensureDiagnosticsStore()
	if err != nil {
		return err
	}
	defer closer()

	books, err := store.GetAllBooksCore(limit, 0)
	if err != nil {
		return fmt.Errorf("failed to fetch books: %w", err)
	}
	if len(books) == 0 {
		fmt.Println("No books found.")
		return nil
	}

	for i, book := range books {
		fmt.Printf("%2d. ID: %s\n", i+1, book.ID)
		fmt.Printf("    Title: %s\n", book.Title)
		fmt.Printf("    FilePath: %s\n", book.FilePath)
		if book.FileHash != nil {
			fmt.Printf("    FileHash: %s\n", *book.FileHash)
		}
		if book.OriginalFileHash != nil {
			fmt.Printf("    OriginalHash: %s\n", *book.OriginalFileHash)
		}
		if book.OrganizedFileHash != nil {
			fmt.Printf("    OrganizedHash: %s\n", *book.OrganizedFileHash)
		}
		fmt.Println("---")
	}

	return nil
}

func runRawPebbleQuery(limit int, prefix string) error {
	db, err := pebble.Open(config.AppConfig.DatabasePath, &pebble.Options{
		FormatMajorVersion: pebble.FormatNewest,
	})
	if err != nil {
		return fmt.Errorf("failed to open Pebble database: %w", err)
	}
	defer db.Close()

	iterOpts := &pebble.IterOptions{}
	if prefix != "" {
		iterOpts.LowerBound = []byte(prefix)
		iterOpts.UpperBound = append([]byte(prefix), 0xFF)
	}

	iter, err := db.NewIter(iterOpts)
	if err != nil {
		return fmt.Errorf("failed to create iterator: %w", err)
	}
	defer iter.Close()

	count := 0
	ok := iter.First()
	if prefix != "" {
		ok = iter.SeekGE([]byte(prefix))
	}

	for ; ok && iter.Valid(); ok = iter.Next() {
		fmt.Printf("Key: %s\n", string(iter.Key()))
		val := iter.Value()
		fmt.Printf("Value length: %d bytes\n", len(val))
		preview := truncateString(string(val), 500)
		fmt.Printf("Value preview: %s\n", preview)
		fmt.Println("---")

		count++
		if count >= limit {
			break
		}
	}

	if err := iter.Error(); err != nil {
		return fmt.Errorf("iterator error: %w", err)
	}

	if count == 0 {
		fmt.Println("No keys matched the requested prefix.")
	}

	return nil
}

func hasPlaceholder(path string, tokens []string) bool {
	lower := strings.ToLower(path)
	for _, token := range tokens {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}

func promptYesNo(action string) (bool, error) {
	fmt.Printf("%s? Type 'yes' to confirm: ", action)
	reader := bufio.NewReader(os.Stdin)
	response, err := reader.ReadString('\n')
	if err != nil {
		return false, err
	}
	response = strings.TrimSpace(strings.ToLower(response))
	return response == "yes", nil
}

func truncateString(in string, max int) string {
	if len(in) <= max {
		return in
	}
	return in[:max] + "..."
}
