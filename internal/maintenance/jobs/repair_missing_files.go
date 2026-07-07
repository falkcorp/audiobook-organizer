// file: internal/maintenance/jobs/repair_missing_files.go
// version: 1.5.0
// guid: f1a7b5e6-8c9d-0e1f-2a3b-4c5d6e7f8a90
// last-edited: 2026-07-07

package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/util"
)

func init() { maintenance.Register(&repairMissingFilesJob{}) }

type repairMissingFilesJob struct{}

func (j *repairMissingFilesJob) ID() string       { return "repair-missing-files" }
func (j *repairMissingFilesJob) Name() string     { return "Repair Missing Files" }
func (j *repairMissingFilesJob) Category() string { return "Files" }
func (j *repairMissingFilesJob) Description() string {
	return "Tries to locate book_files whose stored path no longer exists and updates the DB record with the new path"
}
func (j *repairMissingFilesJob) DefaultParams() any {
	return struct {
		DryRun bool `json:"dry_run"`
	}{DryRun: true}
}
func (j *repairMissingFilesJob) CanResume() bool { return true }

func (j *repairMissingFilesJob) Run(ctx context.Context, store database.Store, reporter maintenance.ProgressReporter, dryRun bool) error {
	opID := maintenance.OperationIDFromCtx(ctx)

	searchRoots := rmfr_searchRoots()

	allFiles, err := store.GetAllBookFilesCore()
	if err != nil {
		return fmt.Errorf("GetAllBookFilesCore: %w", err)
	}
	allBooks, err := store.GetAllBooksCore(0, 0)
	if err != nil {
		return fmt.Errorf("GetAllBooksCore: %w", err)
	}
	allAuthors, err := store.GetAllAuthors()
	if err != nil {
		return fmt.Errorf("GetAllAuthors: %w", err)
	}
	authorByID := make(map[int]string, len(allAuthors))
	for _, a := range allAuthors {
		authorByID[a.ID] = a.Name
	}
	metaByBook := make(map[string]rmfr_bookMeta, len(allBooks))
	for i := range allBooks {
		b := &allBooks[i]
		author := ""
		if b.AuthorID != nil {
			author = authorByID[*b.AuthorID]
		}
		metaByBook[b.ID] = rmfr_bookMeta{title: b.Title, author: author}
	}

	// Collect candidates.
	var candidates []database.BookFileCore
	for i := range allFiles {
		f := &allFiles[i]
		if f.FilePath == "" || f.Missing {
			continue
		}
		if _, statErr := os.Stat(f.FilePath); statErr == nil {
			continue
		}
		candidates = append(candidates, *f)
	}

	// Skip already-processed files from a prior run.
	var existingResults []database.OperationResult
	if opID != "" {
		existingResults, _ = store.GetOperationResults(opID)
	}
	done := make(map[string]bool, len(existingResults))
	for _, r := range existingResults {
		done[r.BookID] = true
	}
	var work []database.BookFileCore
	for _, f := range candidates {
		if !done[f.ID] {
			work = append(work, f)
		}
	}

	totalFiles := len(existingResults) + len(work)
	alreadyDone := len(existingResults)
	slog.Info("repair-missing-files candidates, already done, to process", "opID", opID, "totalFiles", totalFiles, "alreadyDone", alreadyDone, "work_count", len(work))

	reporter.SetTotal(totalFiles)
	for i := 0; i < alreadyDone; i++ {
		reporter.Increment()
	}

	if len(work) == 0 {
		slog.Info("all files already processed")
		return nil
	}

	// Parse iTunes XML for PID lookups.
	pidToLocation := make(map[string]string)
	if xmlPath := config.AppConfig.ITunes.LibraryReadPath; xmlPath != "" {
		if lib, parseErr := itunes.ParseLibrary(xmlPath); parseErr != nil {
			slog.Warn("repair-missing-files iTunes XML parse error", "opID", opID, "parseErr", parseErr)
		} else {
			for _, track := range lib.Tracks {
				if track.PersistentID != "" && track.Location != "" {
					pidToLocation[track.PersistentID] = track.Location
				}
			}
			slog.Info("repair-missing-files loaded PID→location entries", "opID", opID, "pidToLocation_count", len(pidToLocation))
		}
	}

	itunesOpts := itunes.ImportOptions{PathMappings: make([]itunes.PathMapping, len(config.AppConfig.ITunes.PathMappings))}
	for i, m := range config.AppConfig.ITunes.PathMappings {
		itunesOpts.PathMappings[i] = itunes.PathMapping{From: m.From, To: m.To}
	}

	audioExts := map[string]bool{".m4b": true, ".m4a": true, ".mp3": true, ".flac": true, ".ogg": true, ".opus": true}

	var filenameIdx map[string][]string
	var idxOnce sync.Once
	var idxMu sync.Mutex
	buildIdx := func() {
		idxOnce.Do(func() {
			slog.Info("building filename index…")
			idx := make(map[string][]string, 200000)
			for _, root := range searchRoots {
				_ = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
					if walkErr != nil || d.IsDir() {
						return nil
					}
					if audioExts[strings.ToLower(filepath.Ext(path))] {
						base := filepath.Base(path)
						idx[base] = append(idx[base], path)
					}
					return nil
				})
			}
			idxMu.Lock()
			filenameIdx = idx
			idxMu.Unlock()
			slog.Info("repair-missing-files filename index built ( unique names)", "opID", opID, "idx_count", len(idx))
		})
	}
	getIdx := func() map[string][]string {
		idxMu.Lock()
		defer idxMu.Unlock()
		return filenameIdx
	}

	var completed int64 = int64(alreadyDone)
	var progressMu sync.Mutex

	workCh := make(chan database.BookFileCore, len(work))
	for _, f := range work {
		workCh <- f
	}
	close(workCh)

	var wg sync.WaitGroup
	const workers = 4
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range workCh {
				if ctx.Err() != nil {
					return
				}
				res := rmfr_repairOne(f, metaByBook, pidToLocation, itunesOpts, dryRun, searchRoots, audioExts, buildIdx, getIdx, store, opID)

				if opID != "" {
					resultJSON, _ := json.Marshal(res)
					_ = store.CreateOperationResult(&database.OperationResult{
						OperationID: opID,
						BookID:      f.ID,
						ResultJSON:  string(resultJSON),
						Status:      res.Method,
					})
				}

				atomic.AddInt64(&completed, 1)
				progressMu.Lock()
				reporter.Increment()
				progressMu.Unlock()
			}
		}()
	}
	wg.Wait()

	finalCount := atomic.LoadInt64(&completed)
	msg := fmt.Sprintf("Repaired %d of %d missing files", finalCount, totalFiles)
	slog.Info(msg)
	slog.Info("repair-missing-files finished / files", "opID", opID, "finalCount", finalCount, "totalFiles", totalFiles)
	return nil
}

// rmfr_searchRoots builds the ordered list of roots to search from config.
func rmfr_searchRoots() []string {
	roots := []string{config.AppConfig.ITunes.MediaRoot, config.AppConfig.RootDir}
	var out []string
	for _, r := range roots {
		if r != "" {
			out = append(out, filepath.Clean(r))
		}
	}
	return out
}

type rmfr_bookMeta struct {
	title  string
	author string
}

type rmfr_result struct {
	FileID  string `json:"file_id"`
	BookID  string `json:"book_id"`
	Title   string `json:"book_title"`
	OldPath string `json:"old_path"`
	NewPath string `json:"new_path,omitempty"`
	Method  string `json:"method"`
	Matches int    `json:"matches,omitempty"`
	Applied bool   `json:"applied"`
	Error   string `json:"error,omitempty"`
}

// rmfr_repairOne tries four escalating strategies and returns a result.
// Only calls UpdateBookFile — never creates new Book or BookFile rows. On a
// successful match it hydrates the full row via GetBookFiles(f.BookID) and
// writes THAT — never a hand-built BookFile{} from the Core candidate, which
// would wipe the stored fingerprint. See
// docs/audits/2026-07-05-updatebookfile-memdb-writeback-fingerprint-wipe.md.
func rmfr_repairOne(
	f database.BookFileCore,
	metaByBook map[string]rmfr_bookMeta,
	pidToLocation map[string]string,
	itunesOpts itunes.ImportOptions,
	dryRun bool,
	searchRoots []string,
	audioExts map[string]bool,
	buildIdx func(),
	getIdx func() map[string][]string,
	store database.Store,
	opID string,
) rmfr_result {
	bm := metaByBook[f.BookID]
	res := rmfr_result{
		FileID:  f.ID,
		BookID:  f.BookID,
		Title:   bm.title,
		OldPath: f.FilePath,
	}

	if _, statErr := os.Stat(f.FilePath); statErr == nil {
		res.Method = "skipped"
		return res
	}

	candidate, method := "", ""

	// Tier 1: iTunes PID → XML Location → RemapPath
	if candidate == "" && f.ITunesPersistentID != "" {
		if loc, ok := pidToLocation[f.ITunesPersistentID]; ok {
			remapped := itunesOpts.RemapPath(loc)
			if remapped != "" && remapped != loc {
				if _, statErr := os.Stat(remapped); statErr == nil {
					candidate, method = remapped, "pid"
				}
			}
		}
	}

	// Tier 2: exact basename search across filename index
	if candidate == "" {
		buildIdx()
		base := filepath.Base(f.FilePath)
		idx := getIdx()
		paths := idx[base]
		switch len(paths) {
		case 1:
			candidate, method = paths[0], "filename"
			res.Matches = 1
		case 0:
			// no match
		default:
			parentDir := filepath.Base(filepath.Dir(f.FilePath))
			var narrowed []string
			for _, p := range paths {
				if strings.EqualFold(filepath.Base(filepath.Dir(p)), parentDir) {
					narrowed = append(narrowed, p)
				}
			}
			if len(narrowed) > 1 && bm.author != "" {
				lastName := strings.ToLower(bm.author)
				if i := strings.LastIndex(lastName, " "); i > 0 {
					lastName = lastName[i+1:]
				}
				var n2 []string
				for _, p := range narrowed {
					if strings.Contains(strings.ToLower(filepath.Base(filepath.Dir(filepath.Dir(p)))), lastName) {
						n2 = append(n2, p)
					}
				}
				if len(n2) >= 1 {
					narrowed = n2
				}
			}
			switch len(narrowed) {
			case 1:
				candidate, method = narrowed[0], "filename"
				res.Matches = 1
			case 0:
				// fall through
			default:
				res.Method = "ambiguous"
				res.Matches = len(narrowed)
				return res
			}
		}
	}

	// Tier 3: stem-prefix match in the same directory
	if candidate == "" {
		dir := filepath.Dir(f.FilePath)
		base := filepath.Base(f.FilePath)
		ext := filepath.Ext(base)
		stem := strings.TrimSuffix(base, ext)
		if entries, readErr := os.ReadDir(dir); readErr == nil {
			for _, de := range entries {
				if de.IsDir() {
					continue
				}
				name := de.Name()
				nameExt := filepath.Ext(name)
				nameStem := strings.TrimSuffix(name, nameExt)
				if strings.EqualFold(nameExt, ext) &&
					strings.HasPrefix(nameStem, stem) &&
					name != base &&
					len(nameStem) > len(stem) &&
					nameStem[len(stem)] != ' ' {
					candidate, method = filepath.Join(dir, name), "truncation"
					break
				}
			}
		}
	}

	// Tier 4: author last-name + title-prefixed album dir
	if candidate == "" && bm.author != "" && bm.title != "" {
		lastName := bm.author
		if i := strings.LastIndex(bm.author, " "); i > 0 {
			lastName = bm.author[i+1:]
		}
		titlePrefix := bm.title
		if len(titlePrefix) > 30 {
			titlePrefix = titlePrefix[:30]
		}
		storedBase := filepath.Base(f.FilePath)
		var matches []string
		for _, root := range searchRoots {
			entries, rerr := os.ReadDir(root)
			if rerr != nil {
				continue
			}
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				if !strings.Contains(strings.ToLower(entry.Name()), strings.ToLower(lastName)) {
					continue
				}
				authorDir := filepath.Join(root, entry.Name())
				albumEntries, aErr := os.ReadDir(authorDir)
				if aErr != nil {
					continue
				}
				for _, album := range albumEntries {
					if !album.IsDir() {
						continue
					}
					if !strings.HasPrefix(strings.ToLower(album.Name()), strings.ToLower(titlePrefix)) {
						continue
					}
					exact := filepath.Join(authorDir, album.Name(), storedBase)
					if _, statErr := os.Stat(exact); statErr == nil {
						matches = append(matches, exact)
						continue
					}
					albumFiles, _ := os.ReadDir(filepath.Join(authorDir, album.Name()))
					var audioInAlbum []string
					for _, af := range albumFiles {
						if !af.IsDir() && audioExts[strings.ToLower(filepath.Ext(af.Name()))] {
							audioInAlbum = append(audioInAlbum, filepath.Join(authorDir, album.Name(), af.Name()))
						}
					}
					if len(audioInAlbum) == 1 {
						matches = append(matches, audioInAlbum[0])
					}
				}
			}
		}
		switch len(matches) {
		case 1:
			candidate, method = matches[0], "author_title"
			res.Matches = 1
		case 0:
			// no match
		default:
			res.Method = "ambiguous"
			res.Matches = len(matches)
			return res
		}
	}

	// Tier 4b: flat iTunes library — audio files directly in the author dir
	if candidate == "" && bm.author != "" {
		lastName := bm.author
		if i := strings.LastIndex(bm.author, " "); i > 0 {
			lastName = bm.author[i+1:]
		}
		storedBase := filepath.Base(f.FilePath)
		storedStem := strings.TrimSuffix(storedBase, filepath.Ext(storedBase))
		titleFromFile := storedStem
		if i := strings.IndexByte(storedStem, ' '); i > 0 {
			prefix := storedStem[:i]
			isNum := true
			for _, r := range prefix {
				if r < '0' || r > '9' {
					isNum = false
					break
				}
			}
			if isNum {
				titleFromFile = strings.TrimSpace(storedStem[i+1:])
			}
		}

		var matches []string
		for _, root := range searchRoots {
			entries, rerr := os.ReadDir(root)
			if rerr != nil {
				continue
			}
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				if !strings.Contains(strings.ToLower(entry.Name()), strings.ToLower(lastName)) {
					continue
				}
				authorDir := filepath.Join(root, entry.Name())
				dirFiles, _ := os.ReadDir(authorDir)
				for _, df := range dirFiles {
					if df.IsDir() || !audioExts[strings.ToLower(filepath.Ext(df.Name()))] {
						continue
					}
					fileStem := strings.TrimSuffix(df.Name(), filepath.Ext(df.Name()))
					if strings.EqualFold(fileStem, titleFromFile) {
						matches = append(matches, filepath.Join(authorDir, df.Name()))
					}
				}
			}
		}
		if len(matches) > 1 {
			authorLower := strings.ToLower(bm.author)
			var preferred []string
			for _, m := range matches {
				dirName := strings.ToLower(filepath.Base(filepath.Dir(m)))
				if strings.HasPrefix(dirName, authorLower) {
					preferred = append(preferred, m)
				}
			}
			if len(preferred) == 1 {
				matches = preferred
			}
		}
		switch len(matches) {
		case 1:
			candidate, method = matches[0], "flat_stem"
			res.Matches = 1
		case 0:
			// no match
		default:
			res.Method = "ambiguous"
			res.Matches = len(matches)
			return res
		}
	}

	if candidate == "" {
		res.Method = "unresolved"
		return res
	}

	candidate = filepath.Clean(candidate)
	withinARoot := false
	for _, root := range searchRoots {
		if util.WithinRoot(candidate, root) {
			withinARoot = true
			break
		}
	}
	if !withinARoot {
		slog.Warn("repair-missing-files candidate outside all search roots, skipping", "opID", opID, "candidate", candidate)
		res.Method = "unresolved"
		return res
	}

	res.NewPath = candidate
	res.Method = method
	res.Matches = 1

	if dryRun {
		return res
	}

	fi, _ := os.Stat(candidate)

	// Hydrate the full row and mutate/write THAT — see the doc comment above.
	full, herr := store.GetBookFiles(f.BookID)
	if herr != nil {
		res.Error = herr.Error()
		slog.Warn("repair-missing-files hydrate failed", "opID", opID, "f", f.ID, "herr", herr)
		return res
	}
	var target *database.BookFile
	for j := range full {
		if full[j].ID == f.ID {
			target = &full[j]
			break
		}
	}
	if target == nil {
		res.Error = "hydrate: row not found"
		slog.Warn("repair-missing-files hydrate: row not found", "opID", opID, "f", f.ID)
		return res
	}
	target.FilePath = candidate
	target.OriginalFilename = filepath.Base(candidate)
	target.Missing = false
	if fi != nil {
		target.FileSize = fi.Size()
	}
	if ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(candidate), ".")); ext != "" {
		target.Format = ext
	}
	if upErr := store.UpdateBookFile(target.ID, target); upErr != nil {
		res.Error = upErr.Error()
		slog.Warn("repair-missing-files UpdateBookFile", "opID", opID, "f", f.ID, "upErr", upErr)
	} else {
		res.Applied = true
	}
	return res
}
