// file: internal/maintenance/jobs/cleanup_organize_mess.go
// version: 2.3.0
// guid: a1000007-0000-0000-0000-000000000007
// last-edited: 2026-08-17

package jobs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"log/slog"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
)

func init() { maintenance.Register(&cleanupOrganizeMess{}) }

type cleanupOrganizeMess struct{}

func (j *cleanupOrganizeMess) ID() string       { return "cleanup-organize-mess" }
func (j *cleanupOrganizeMess) Name() string     { return "Cleanup Organize Mess" }
func (j *cleanupOrganizeMess) Category() string { return "cleanup" }
func (j *cleanupOrganizeMess) DefaultParams() any {
	return struct {
		DryRun bool `json:"dry_run"`
	}{DryRun: true}
}
func (j *cleanupOrganizeMess) Description() string {
	return "Clean up leftover organize artifacts and garbage directories"
}
func (j *cleanupOrganizeMess) CanResume() bool { return false }

func (j *cleanupOrganizeMess) Run(ctx context.Context, _ maintenance.JobStore, reporter maintenance.ProgressReporter, dryRun bool) error {
	rootDir := config.AppConfig.RootDir
	if rootDir == "" {
		return fmt.Errorf("root_dir is not configured")
	}
	if _, err := os.Stat(rootDir); err != nil {
		return fmt.Errorf("root_dir not accessible: %w", err)
	}

	var dirs []string
	walkErr := filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			return nil
		}
		if path == rootDir {
			return nil
		}
		if strings.HasPrefix(filepath.Base(path), ".") {
			return filepath.SkipDir
		}
		dirs = append(dirs, path)
		return nil
	})
	if walkErr != nil {
		return fmt.Errorf("failed to walk root directory: %w", walkErr)
	}

	sort.Slice(dirs, func(i, j int) bool {
		return len(dirs[i]) > len(dirs[j])
	})

	reporter.SetTotal(len(dirs))

	emptyRemoved := 0
	garbageFound := 0

	for _, dir := range dirs {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		name := filepath.Base(dir)
		if reason := comIsGarbageDirectory(name); reason != "" {
			garbageFound++
			slog.Warn("Garbage dir found", "dir", dir, "reason", reason)
			msg := fmt.Sprintf("Garbage directory: %s (%s)", dir, reason)
			reporter.Log("warn", msg, nil)
		}

		empty, checkErr := comIsDirEmpty(dir)
		if checkErr != nil {
			msg := checkErr.Error()
			slog.Error("Stat error", "dir", dir, "err", checkErr)
			reporter.Log("error", "Failed to check directory: "+dir, &msg)
			reporter.Increment()
			continue
		}
		if !empty {
			reporter.Increment()
			continue
		}

		if !dryRun {
			if removeErr := os.Remove(dir); removeErr != nil {
				msg := removeErr.Error()
				slog.Error("Failed to remove", "dir", dir, "err", removeErr)
				reporter.Log("error", "Failed to remove empty directory: "+dir, &msg)
			} else {
				emptyRemoved++
			}
		} else {
			emptyRemoved++
		}
		reporter.Increment()
	}

	slog.Info("Done garbage_dirs empty_removed dryRun", "garbageFound", garbageFound, "emptyRemoved", emptyRemoved, "dryRun", dryRun)
	return nil
}

func comIsDirEmpty(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), ".") {
			return false, nil
		}
	}
	return true, nil
}

func comIsGarbageDirectory(name string) string {
	if name == "" {
		return ""
	}
	chapterFragmentRe := regexp.MustCompile(`^\d{1,3}[_ ][_\-\s]`)
	if chapterFragmentRe.MatchString(name) {
		return "starts with chapter number fragment"
	}
	pureNumericRe := regexp.MustCompile(`^\d+$`)
	if pureNumericRe.MatchString(name) {
		return "purely numeric directory name"
	}
	doubleSegmentRe := regexp.MustCompile(` - \d{1,3} - `)
	if doubleSegmentRe.MatchString(name) {
		return "contains double-nested chapter segment pattern"
	}
	trimmed := strings.TrimSpace(name)
	if len([]rune(trimmed)) <= 2 && !comAllAlpha(trimmed) {
		return "suspiciously short non-alphabetic directory name"
	}
	return ""
}

func comAllAlpha(s string) bool {
	for _, r := range s {
		if !unicode.IsLetter(r) {
			return false
		}
	}
	return len(s) > 0
}

// Policy declares the bridge's existing behaviour verbatim: see DefaultPolicy.
func (j *cleanupOrganizeMess) Policy() maintenance.ExecutionPolicy {
	return maintenance.DefaultPolicy()
}
