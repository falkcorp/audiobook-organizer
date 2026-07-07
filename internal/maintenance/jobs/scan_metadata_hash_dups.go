// file: internal/maintenance/jobs/scan_metadata_hash_dups.go
// version: 1.2.0
// guid: a1000017-0000-0000-0000-000000000017
// last-edited: 2026-07-07

package jobs

import (
	"context"
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"log/slog"
)

func init() { maintenance.Register(&scanMetadataHashDupsJob{}) }

type scanMetadataHashDupsJob struct{}

func (j *scanMetadataHashDupsJob) ID() string       { return "scan-metadata-hash-dups" }
func (j *scanMetadataHashDupsJob) Name() string     { return "Scan Metadata Hash Dups" }
func (j *scanMetadataHashDupsJob) Category() string { return "dedup" }
func (j *scanMetadataHashDupsJob) DefaultParams() any {
	return struct {
		DryRun bool `json:"dry_run"`
	}{DryRun: false}
}
func (j *scanMetadataHashDupsJob) Description() string {
	return "Scan for books sharing the same metadata source hash"
}
func (j *scanMetadataHashDupsJob) CanResume() bool { return false }
func (j *scanMetadataHashDupsJob) Run(ctx context.Context, store database.Store, reporter maintenance.ProgressReporter, _ bool) error {
	books, err := store.GetAllBooksCore(0, 0)
	if err != nil {
		return err
	}
	reporter.SetTotal(len(books))
	byHash := map[string][]string{}
	for i := range books {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		reporter.Increment()
		if books[i].MetadataSourceHash == nil || *books[i].MetadataSourceHash == "" {
			continue
		}
		byHash[*books[i].MetadataSourceHash] = append(byHash[*books[i].MetadataSourceHash], books[i].ID)
	}
	dups := 0
	for _, ids := range byHash {
		if len(ids) > 1 {
			dups++
			detail := fmt.Sprintf("book_ids=%v", ids)
			slog.Warn("metadata hash duplicate group", "details", detail)
		}
	}
	slog.Info("scan-metadata-hash-dups complete duplicate groups", "dups", dups)
	return nil
}
