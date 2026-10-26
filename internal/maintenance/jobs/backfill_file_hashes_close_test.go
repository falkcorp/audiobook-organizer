// file: internal/maintenance/jobs/backfill_file_hashes_close_test.go
// version: 1.3.0
// guid: 0c6d4a82-91e7-4b35-8f2a-d3a7b5e1c649
// last-edited: 2026-10-26

package jobs

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failingCloseFile is a real report file whose Close reports an error after
// closing it, the way a full or failing disk can at the very end of a run.
type failingCloseFile struct{ hbFile }

func (f failingCloseFile) Close() error {
	_ = f.hbFile.Close()
	return errors.New("close: input/output error")
}

type closeTestReporter struct{ logs []string }

func (r *closeTestReporter) SetTotal(int)                        {}
func (r *closeTestReporter) Increment()                          {}
func (r *closeTestReporter) Log(_ string, msg string, _ *string) { r.logs = append(r.logs, msg) }

// If closing the report fails after every chunk was written, the op fails and
// says nothing else: no "completed" summary is saved, so the status and the
// summary cannot disagree.
func TestBackfillFileHashes_CloseFailureAfterSuccessFailsTheWholeRun(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "Gotham"), 0o755))
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })
	prevOpen := hbOpenFile
	hbOpenFile = func(name string, flag int, perm os.FileMode) (hbFile, error) {
		f, err := prevOpen(name, flag, perm)
		if err != nil {
			return nil, err
		}
		return failingCloseFile{f}, nil
	}
	t.Cleanup(func() { hbOpenFile = prevOpen })

	state := map[string][]byte{}
	var summaries []*database.OperationSummaryLog
	st := &database.MockStore{
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) {
			return []database.BookFileCore{{ID: "f1", BookID: "b1", FilePath: filepath.Join(root, "Gotham", "Year One.m4b")}}, nil
		},
		SaveOperationStateFunc:   func(id string, data []byte) error { state[id] = data; return nil },
		GetOperationStateFunc:    func(id string) ([]byte, error) { return state[id], nil },
		DeleteOperationStateFunc: func(id string) error { delete(state, id); return nil },
		SaveOperationSummaryLogFunc: func(op *database.OperationSummaryLog) error {
			summaries = append(summaries, op)
			return nil
		},
	}
	const opID = "01CLOSEFAILS"
	rep := &closeTestReporter{}

	err := (&backfillFileHashesJob{}).Run(maintenance.WithOperationID(context.Background(), opID), st, rep, true)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "input/output error")
	assert.Empty(t, summaries, "no completed summary for a run that returns an error")
	for _, l := range rep.logs {
		assert.NotContains(t, l, "hashed=", "no result line either")
	}
}

// failingWriteFile is a real report file whose writes always fail, the way a
// full disk does. It replaces the old FIFO-based test: the injected error is the
// same on every platform and needs no blocked reader.
type failingWriteFile struct{ hbFile }

func (failingWriteFile) Write([]byte) (int, error) {
	return 0, errors.New("write: no space left on device")
}

// A report write failure stops the run with an error that names the report; the
// run does not carry on and claim a complete report.
func TestBackfillFileHashes_ReportWriteFailureStopsTheRun(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "Gotham"), 0o755))
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })
	prevOpen := hbOpenFile
	hbOpenFile = func(name string, flag int, perm os.FileMode) (hbFile, error) {
		f, err := prevOpen(name, flag, perm)
		if err != nil {
			return nil, err
		}
		return failingWriteFile{f}, nil
	}
	t.Cleanup(func() { hbOpenFile = prevOpen })

	var summaries []*database.OperationSummaryLog
	st := &database.MockStore{
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) {
			return []database.BookFileCore{
				{ID: "f1", BookID: "b1", FilePath: filepath.Join(root, "Gotham", "Year One.m4b")},
				{ID: "f2", BookID: "b1", FilePath: filepath.Join(root, "Gotham", "Hush.m4b")},
			}, nil
		},
		SaveOperationStateFunc:   func(string, []byte) error { return nil },
		GetOperationStateFunc:    func(string) ([]byte, error) { return nil, nil },
		DeleteOperationStateFunc: func(string) error { return nil },
		SaveOperationSummaryLogFunc: func(op *database.OperationSummaryLog) error {
			summaries = append(summaries, op)
			return nil
		},
	}
	rep := &closeTestReporter{}

	err := (&backfillFileHashesJob{}).Run(maintenance.WithOperationID(context.Background(), "01WRITEFAILS"), st, rep, true)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "report")
	assert.Contains(t, err.Error(), "no space left on device")
	assert.Empty(t, summaries)
	for _, l := range rep.logs {
		assert.NotContains(t, l, "hashed=", "no result line for a run that failed")
	}
}

// Opening a report never follows a symlink, for a resumable report and for a
// fresh one alike: the open is refused and the file the link points at is
// untouched.
func TestOpenHashBackfillReport_RefusesASymlink(t *testing.T) {
	for _, existing := range []bool{true, false} {
		dir := t.TempDir()
		victim := filepath.Join(dir, "precious.txt")
		require.NoError(t, os.WriteFile(victim, []byte("precious"), 0o600))
		link := filepath.Join(dir, "report.tsv")
		require.NoError(t, os.Symlink(victim, link))

		rep, _, _, err := openHashBackfillReport(link, existing)

		require.Error(t, err, "existing=%v", existing)
		assert.Nil(t, rep)
		got, rerr := os.ReadFile(victim)
		require.NoError(t, rerr)
		assert.Equal(t, "precious", string(got), "existing=%v", existing)
	}
}

// foreignOwnerFile reports a different owner than this process, the way a report
// file another user made would.
type foreignOwnerFile struct{ hbFile }

type foreignOwnerInfo struct{ os.FileInfo }

func (i foreignOwnerInfo) Sys() any {
	st := *(i.FileInfo.Sys().(*syscall.Stat_t))
	st.Uid++
	return &st
}

func (f foreignOwnerFile) Stat() (os.FileInfo, error) {
	info, err := f.hbFile.Stat()
	return foreignOwnerInfo{info}, err
}

// notRegularFile reports a device, not a regular file.
type notRegularFile struct{ hbFile }

type notRegularInfo struct{ os.FileInfo }

func (i notRegularInfo) Mode() os.FileMode { return i.FileInfo.Mode() | fs.ModeDevice }

func (f notRegularFile) Stat() (os.FileInfo, error) {
	info, err := f.hbFile.Stat()
	return notRegularInfo{info}, err
}

func hbWithOpenWrapper(t *testing.T, wrap func(hbFile) hbFile) {
	t.Helper()
	prev := hbOpenFile
	hbOpenFile = func(name string, flag int, perm os.FileMode) (hbFile, error) {
		f, err := prev(name, flag, perm)
		if err != nil {
			return nil, err
		}
		return wrap(f), nil
	}
	t.Cleanup(func() { hbOpenFile = prev })
}

// A report the job did not make (another owner, not a regular file) is refused,
// for a resumable open and a fresh one, and a fresh open does not empty it.
func TestOpenHashBackfillReport_RefusesAFileItDoesNotTrust(t *testing.T) {
	for name, wrap := range map[string]func(hbFile) hbFile{
		"another owner":      func(f hbFile) hbFile { return foreignOwnerFile{f} },
		"not a regular file": func(f hbFile) hbFile { return notRegularFile{f} },
	} {
		for _, existing := range []bool{true, false} {
			t.Run(name, func(t *testing.T) {
				hbWithOpenWrapper(t, wrap)
				path := filepath.Join(t.TempDir(), "report.tsv")
				content := hbReportHeader + "not-found\tf1\tb1\t/lib/x\tfile not found\n"
				require.NoError(t, os.WriteFile(path, []byte(content), 0o640))

				rep, _, _, err := openHashBackfillReport(path, existing)

				require.Error(t, err, "existing=%v", existing)
				assert.Nil(t, rep)
				got, rerr := os.ReadFile(path)
				require.NoError(t, rerr)
				assert.Equal(t, content, string(got), "the file is left as it was (existing=%v)", existing)
			})
		}
	}
}

// A report over the size cap, or with a line over the line cap, is refused
// instead of read.
func TestOpenHashBackfillReport_RefusesAnOversizedReportOrLine(t *testing.T) {
	row := "not-found\tf1\tb1\t/lib/x\tfile not found\n"
	t.Run("size over the cap", func(t *testing.T) {
		prev := hbMaxReportBytes
		hbMaxReportBytes = int64(len(hbReportHeader+row)) - 1
		t.Cleanup(func() { hbMaxReportBytes = prev })
		path := filepath.Join(t.TempDir(), "report.tsv")
		require.NoError(t, os.WriteFile(path, []byte(hbReportHeader+row), 0o640))

		rep, _, _, err := openHashBackfillReport(path, true)

		require.Error(t, err)
		assert.Nil(t, rep)
		assert.Contains(t, err.Error(), "over the")
	})
	t.Run("size at the cap is read", func(t *testing.T) {
		prev := hbMaxReportBytes
		hbMaxReportBytes = int64(len(hbReportHeader + row))
		t.Cleanup(func() { hbMaxReportBytes = prev })
		path := filepath.Join(t.TempDir(), "report.tsv")
		require.NoError(t, os.WriteFile(path, []byte(hbReportHeader+row), 0o640))

		rep, seeded, prior, err := openHashBackfillReport(path, true)

		require.NoError(t, err)
		assert.True(t, prior)
		assert.Equal(t, int64(1), seeded.notFound)
		require.NoError(t, rep.close())
	})
	for name, line := range map[string]string{
		"line over the default cap":           "not-found\tf2\tb1\t/lib/" + strings.Repeat("A", 70<<10) + "\tfile not found\n",
		"final line over the cap, no newline": "not-found\tf2\tb1\t/lib/" + strings.Repeat("A", 70<<10),
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "report.tsv")
			require.NoError(t, os.WriteFile(path, []byte(hbReportHeader+row+line), 0o640))

			rep, _, _, err := openHashBackfillReport(path, true)

			require.Error(t, err)
			assert.Nil(t, rep)
			assert.Contains(t, err.Error(), "longer than")
			got, rerr := os.ReadFile(path)
			require.NoError(t, rerr)
			assert.Equal(t, hbReportHeader+row+line, string(got), "the file is left as it was")
		})
	}
	t.Run("a line just under the cap is read", func(t *testing.T) {
		pad := strings.Repeat("A", 64<<10-len("not-found\tf2\tb1\t\tx\n")-1) // 64 KiB less one byte, whatever the cap variable says
		long := "not-found\tf2\tb1\t" + pad + "\tx\n"
		path := filepath.Join(t.TempDir(), "report.tsv")
		require.NoError(t, os.WriteFile(path, []byte(hbReportHeader+long), 0o640))

		rep, seeded, _, err := openHashBackfillReport(path, true)

		require.NoError(t, err)
		assert.Equal(t, int64(1), seeded.notFound)
		require.NoError(t, rep.close())
	})
}
