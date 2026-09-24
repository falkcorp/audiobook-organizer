// file: internal/versionprimary/signals.go
// version: 1.0.0
// guid: ac90734f-6d4e-405b-8edc-18da688f272b
// last-edited: 2026-09-24

package versionprimary

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/audioutil"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
)

// ChapterProber returns the number of chapters embedded in the file at path.
// Production wires FFprobeChapterCounter; tests pass a stub, so no unit test
// runs a real ffprobe.
type ChapterProber func(ctx context.Context, path string) (int, error)

// FileReader is the book_file read the loader needs.
type FileReader interface {
	GetBookFiles(bookID string) ([]database.BookFile, error)
}

// DefaultProbeTimeout bounds one ffprobe call, as the chapters backfill does.
const DefaultProbeTimeout = 20 * time.Second

// unknownAuthorDir is the folder organize files an author-less book under.
const unknownAuthorDir = "Unknown Author"

// Loader measures Signals for group members. The only disk access is a stat
// of each active file and, for a single m4b/m4a, an ffprobe header read
// (allowed on U0: it decodes no audio). It writes nothing.
type Loader struct {
	Files    FileReader
	Chapters database.ChapterReader
	// RootDir is the library root (config.AppConfig.RootDir). Empty means no
	// member can be under it, so none is eligible.
	RootDir string
	// Probe may be nil: every probe then counts as failed and the chapter
	// table is used.
	Probe        ChapterProber
	ProbeTimeout time.Duration
	// Stat defaults to os.Stat.
	Stat func(string) (os.FileInfo, error)
}

// FFprobeChapterCounter resolves ffprobe once and returns a prober that
// counts the chapters audioutil.ProbeChapters reads.
func FFprobeChapterCounter() (ChapterProber, error) {
	bin, err := audioutil.LookupFFprobe()
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, path string) (int, error) {
		chs, err := audioutil.ProbeChapters(ctx, bin, path)
		if err != nil {
			return 0, err
		}
		return len(chs), nil
	}, nil
}

func isM4BExt(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".m4b", ".m4a":
		return true
	}
	return false
}

// Load measures one member. live is the caller's liveness verdict
// (Electable). An error means the member's files or chapter table could not
// be read; the caller must skip the group rather than decide on a partial
// picture.
func (l Loader) Load(ctx context.Context, b *database.Book, live bool) (Signals, error) {
	s := Signals{Live: live, ChapterSource: ChapterSourceNA}
	files, err := l.Files.GetBookFiles(b.ID)
	if err != nil {
		return s, fmt.Errorf("read book files of %s: %w", b.ID, err)
	}
	stat := l.Stat
	if stat == nil {
		stat = os.Stat
	}
	root := ""
	if strings.TrimSpace(l.RootDir) != "" {
		root = filepath.Clean(l.RootDir)
	}
	var active []database.BookFile
	for _, f := range files {
		if !f.Missing {
			active = append(active, f)
		}
	}
	s.ActiveFiles = len(active)
	s.AllUnderRoot = len(active) > 0 && root != ""
	for _, f := range active {
		p := filepath.Clean(f.FilePath)
		if fi, serr := stat(p); serr != nil || !fi.Mode().IsRegular() {
			s.FilesMissingOnDisk++
		}
		rest, under := "", false
		if root != "" {
			rest, under = pathutil.CutPathPrefix(p, root)
		}
		if !under {
			s.AllUnderRoot = false
		} else {
			for _, seg := range strings.Split(filepath.ToSlash(rest), "/") {
				if strings.EqualFold(seg, unknownAuthorDir) {
					s.UnknownAuthorPath = true
				}
			}
		}
		if f.BitrateKbps > s.BitrateKbps {
			s.BitrateKbps = f.BitrateKbps
		}
	}
	if rt := database.ComputeBookRuntime(b, files); rt.Complete() {
		if sec, ok := rt.KnownSeconds(); ok {
			s.RuntimeSec = sec
		}
	}
	s.SingleM4B = len(active) == 1 && isM4BExt(active[0].FilePath)
	if !s.SingleM4B || s.FilesMissingOnDisk > 0 {
		return s, nil
	}

	if l.Probe != nil {
		timeout := l.ProbeTimeout
		if timeout <= 0 {
			timeout = DefaultProbeTimeout
		}
		pctx, cancel := context.WithTimeout(ctx, timeout)
		n, perr := l.Probe(pctx, active[0].FilePath)
		cancel()
		if perr == nil {
			s.Chapters, s.ChapterSource = n, ChapterSourceProbe
			return s, nil
		}
		if err := ctx.Err(); err != nil {
			return s, err
		}
	}
	chs, cerr := l.Chapters.GetChaptersForBook(b.ID)
	if cerr != nil {
		return s, fmt.Errorf("read chapter table of %s: %w", b.ID, cerr)
	}
	if len(chs) > 0 {
		s.Chapters, s.ChapterSource = len(chs), ChapterSourceTable
	} else {
		s.ChapterSource = ChapterSourceUnknown
	}
	return s, nil
}

// LoadMembers measures every member of a group. alive decides liveness for
// merge losers (Electable).
func (l Loader) LoadMembers(ctx context.Context, books []database.Book, alive func(id string) bool) ([]Member, error) {
	out := make([]Member, 0, len(books))
	for i := range books {
		b := &books[i]
		s, err := l.Load(ctx, b, Electable(b, alive))
		if err != nil {
			return nil, err
		}
		out = append(out, Member{Book: b, Signals: s})
	}
	return out, nil
}
