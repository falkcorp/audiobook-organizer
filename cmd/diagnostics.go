// file: cmd/diagnostics.go
// version: 1.7.2
// guid: c8f6a0d4-2a8b-48cf-9d08-02cc9915d9fc
// last-edited: 2026-11-09

package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/cockroachdb/pebble/v2"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/mediacore/storage"
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

	reservedPrefsCmd = &cobra.Command{
		Use:   "reserved-prefs",
		Short: "Show, delete or rewrite the reserved preference rows (storage_format, storage_migration, db_version, migration_<n>)",
		Long: `Recovery for a store that will not open because a reserved preference row
holds a bad value. It opens Pebble directly, bypassing the storage-format guard
(and passing no Pebble format version, so nothing ratchets), so it works on a
store the guard refuses. Stop the service first.

With no flag it lists the rows and the storage format sidecar. --delete KEY
removes one row; --set KEY=N rewrites storage_format or db_version as a valid
row. A change asks for confirmation (or --yes) and prints exactly what it
changed. Deleting storage_format also removes the sidecar: the next open
restamps a store with data as format 1.

--delete and --set run only as the state directory's owner, the user the
service runs as (sudo -u <that user>): they open the store read-write, and
the files that open writes must be the service's to replace. Listing opens
it read-only and adds nothing to the store, so that rule does not apply to
it; like any open of the store, it still needs the service's user or root to
open the store's LOCK file.`,
		// A refusal is one line: no usage text, and no second "Error:" copy
		// from cobra (main.go already prints the returned error).
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			flags := cmd.Flags()
			del, err := flags.GetString("delete")
			if err != nil {
				return err
			}
			set, err := flags.GetString("set")
			if err != nil {
				return err
			}
			yes, err := flags.GetBool("yes")
			if err != nil {
				return err
			}
			all, err := flags.GetBool("all")
			if err != nil {
				return err
			}
			return runReservedPrefs(commandContext(cmd), config.AppConfig.DatabasePath, del, set, yes, all, promptYesNo)
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

	reservedPrefsCmd.Flags().String("delete", "", "Delete this reserved preference row")
	reservedPrefsCmd.Flags().String("set", "", "Rewrite storage_format or db_version: KEY=N")
	reservedPrefsCmd.Flags().Bool("yes", false, "Skip the confirmation prompt")
	reservedPrefsCmd.Flags().Bool("all", false, "List every migration_<n> row instead of a count")

	diagnosticsCmd.AddCommand(cleanupCmd)
	diagnosticsCmd.AddCommand(reservedPrefsCmd)
	diagnosticsCmd.AddCommand(queryCmd)
}

// diagnosticsCLIStore is the two methods the two diagnostics subcommands make
// on the store. Was database.Store (398 methods) until 2026-08-19; enumerated
// with an empty-interface compiler probe under -gcflags=-e. The --raw query path
// resolves its concrete Pebble handle through database.AsPebbleStore, which
// takes any, so it does not widen this.
type diagnosticsCLIStore interface {
	GetAllBooksCore(limit, offset int) ([]database.BookCore, error)
	DeleteBook(id string) error
}

// ensureDiagnosticsStore opens the store through openCLIStore: read-only for
// the commands that only read (query), read-write for the one that deletes
// (cleanup-invalid).
func ensureDiagnosticsStore(readOnly bool) (diagnosticsCLIStore, func(), error) {
	store, closer, err := openCLIStore(context.Background(), readOnly)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to initialize database: %w", err)
	}
	return store, closer, nil
}

func runCleanupInvalidBooks(force, dryRun bool) error {
	store, closer, err := ensureDiagnosticsStore(false)
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

	store, closer, err := ensureDiagnosticsStore(true)
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
	// Exempt from the guard, not from the swap slot: a store a staged or
	// in-flight restore or reset is about to replace, or has half replaced,
	// is not inspected (refuseBusySwapSlot).
	if _, err := refuseBusySwapSlot(); err != nil {
		return err
	}
	// Raw mode is exempt from the storage-format guard by design (storage
	// efficiency design, section 9): it only reads, and it must work on a
	// store the guard refuses. It passes no FormatMajorVersion, which Pebble
	// resolves to FormatMinSupported; Open only ratchets UP to the requested
	// version, so an inspection can never raise the on-disk format. (Passing
	// PebbleFormatMajorVersion here would raise an older store to the pin.)
	// It opens read-only (R1-25 follow-up N-3): a read-write open writes a
	// new WAL, manifest and OPTIONS file, which, run as root, the service
	// could not replace, and creates an empty store at a mistyped path.
	// Read-only, it adds nothing to the store directory, so the owner rule
	// does not apply: run as root, it leaves nothing the service cannot
	// replace. Like every Pebble open, read-only included, it still opens
	// the store's existing LOCK file for writing, so in practice it runs as
	// the service's user or root.
	db, err := pebble.Open(config.AppConfig.DatabasePath, &pebble.Options{ReadOnly: true})
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

// reservedPrefsLog records every change `diagnostics reserved-prefs` makes,
// in addition to the stdout report.
var reservedPrefsLog = logger.New("cli.diagnostics.reserved-prefs")

func runReservedPrefs(ctx context.Context, dbPath, del, set string, yes, all bool, confirm func(string) (bool, error)) error {
	if dbPath == "" {
		return errors.New("database path is not set (--db / DATABASE_PATH)")
	}
	if del != "" && set != "" {
		return errors.New("use --delete or --set, not both")
	}
	// This recovery tool bypasses the guard on purpose, but not the swap
	// slot: a --set or --delete on a half-swapped directory would write to
	// the wrong store, and a listing would show it as if it were the live
	// one. Every mode refuses on a busy slot before any Pebble open.
	stateDir, err := refuseBusySwapSlot()
	if err != nil {
		return err
	}
	if del == "" && set == "" {
		rows, sidecar, sidecarPresent, err := database.ListReservedPreferences(dbPath)
		if err != nil {
			return err
		}
		fmt.Printf("Reserved preference rows in %s:\n", dbPath)
		if len(rows) == 0 {
			fmt.Println("  (none)")
		}
		migrationRows := 0
		for _, r := range rows {
			if strings.HasPrefix(r.Key, "migration_") && !all {
				migrationRows++
				continue
			}
			fmt.Printf("  %s = %s\n", r.Key, truncateString(r.Raw, 300))
		}
		if migrationRows > 0 {
			fmt.Printf("  migration_<n>: %d rows (--all lists them)\n", migrationRows)
		}
		if sidecarPresent {
			fmt.Printf("Sidecar %s = %q\n", database.StorageFormatSidecarPath(dbPath), sidecar)
		} else {
			fmt.Printf("Sidecar %s is absent\n", database.StorageFormatSidecarPath(dbPath))
		}
		return nil
	}

	key, version, action, flag := del, 0, "Delete", "--delete"
	if set != "" {
		k, v, ok := strings.Cut(set, "=")
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if !ok || err != nil || n < 1 {
			return fmt.Errorf("--set wants KEY=N with N >= 1, got %q", set)
		}
		key, version, action, flag = strings.TrimSpace(k), n, fmt.Sprintf("Rewrite as version %d", n), "--set"
	}
	if !database.IsReservedPreferenceKey(key) {
		return fmt.Errorf("%q is not a reserved preference key", key)
	}
	// The owner rule (seams.d/R1-25.md D29), as app.OpenOffline applies it
	// to a read-write open, and before the prompt, so nobody confirms a
	// change that is then refused: the repair opens the store read-write,
	// and the WAL, manifest and OPTIONS files that open writes, and the
	// sidecar a storage_format change rewrites, belong to whoever runs it.
	// Run as root, they are files the service must replace and cannot. A
	// listing opens read-only and is not refused.
	if err := storage.RefuseNotStateDirOwner(ctx, stateDir, "diagnostics reserved-prefs "+flag); err != nil {
		return err
	}
	if !yes {
		ok, err := confirm(fmt.Sprintf("%s the reserved preference %q in %s (stop the service first)", action, key, dbPath))
		if errors.Is(err, io.EOF) {
			fmt.Println()
			return errors.New("confirmation needed: pass --yes or run interactively")
		}
		if err != nil {
			return err
		}
		if !ok {
			fmt.Println("Aborted. Nothing changed.")
			return nil
		}
	}
	change, err := repairReservedPreference(dbPath, key, version)
	// Report whatever was committed EVEN WHEN err is set: an error after the
	// commit (sidecar removal, closing the store) means the row DID change,
	// and the operator must see exactly what.
	if change.Committed {
		reportReservedChange(dbPath, change)
	}
	if err != nil {
		if change.Committed {
			return fmt.Errorf("the change above WAS committed, but: %w", err)
		}
		return err
	}
	return nil
}

// repairReservedPreference is database.RepairReservedPreference; a variable so
// a test can return a committed change together with an error.
var repairReservedPreference = database.RepairReservedPreference

// reportReservedChange prints every key a repair changed and WARN-logs it.
func reportReservedChange(dbPath string, change database.ReservedPreferenceChange) {
	describe := func(v string, present bool) string {
		if !present {
			return "<absent>"
		}
		return fmt.Sprintf("%q", v)
	}
	fmt.Printf("Changed preference %s in %s: before=%s after=%s\n", change.Key, dbPath,
		describe(change.Before, change.BeforePresent), describe(change.After, change.AfterPresent))
	reservedPrefsLog.Warn("reserved preference changed: db=%s key=%s before=%s after=%s",
		logger.SanitizeLogValue(dbPath), logger.SanitizeLogValue(change.Key),
		logger.SanitizeLogValue(describe(change.Before, change.BeforePresent)),
		logger.SanitizeLogValue(describe(change.After, change.AfterPresent)))
	if change.CounterTouched {
		fmt.Printf("Changed counter:preference in %s: before=%q after=%q (allocated the row's ID)\n",
			dbPath, change.CounterBefore, change.CounterAfter)
		reservedPrefsLog.Warn("preference counter changed: db=%s key=counter:preference before=%s after=%s",
			logger.SanitizeLogValue(dbPath), logger.SanitizeLogValue(change.CounterBefore),
			logger.SanitizeLogValue(change.CounterAfter))
	}
	if change.SidecarTouched {
		fmt.Printf("Changed sidecar %s: before=%s after=%s\n", change.SidecarPath,
			describe(change.SidecarBefore, change.SidecarBeforePresent),
			describe(change.SidecarAfter, change.SidecarAfterPresent))
		reservedPrefsLog.Warn("storage format sidecar changed: path=%s before=%s after=%s",
			logger.SanitizeLogValue(change.SidecarPath),
			logger.SanitizeLogValue(describe(change.SidecarBefore, change.SidecarBeforePresent)),
			logger.SanitizeLogValue(describe(change.SidecarAfter, change.SidecarAfterPresent)))
	}
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
