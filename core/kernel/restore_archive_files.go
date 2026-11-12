// file: core/kernel/restore_archive_files.go
// version: 1.0.1
// guid: 3e8f5a17-9c42-4d0b-b6a1-7f2d0e9c4a58
// last-edited: 2026-11-12

package kernel

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/backup"
	"github.com/falkcorp/mediacore/internal/backuparchive"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// The two state files an archive does not carry and storage restore-offline
// --archive puts back from the operator's own copies (05-backup §5B.3.4 "What
// comes back is one unit"; 05-config §2.4's state-file table). The labels are
// the table's; a restore puts each one back at the path the app declared for
// that label in Config.StorageStateFiles, else at the table's default.
const (
	keyringLabel = "keyring"
	gapFillLabel = "gap_fill"
	// keyringFileName is core.secrets.key_file's default in the state
	// directory, the file the offline secrets commands use too.
	keyringFileName = "secrets.keyring"
	// gapFileName is HEAD's gap-fill file, beside the database.
	gapFileName = "config.yaml"
	// maxOperatorFileBytes bounds both files; secrets reads a keyring no
	// larger either.
	maxOperatorFileBytes = 1 << 20
)

// warnLegacyNoGapFill is what a legacy archive restored without --gap-fill
// warns (05-config §1.12.2, DATA-15; the staged restore's backup.WarnGapFill
// says the same for the same archive).
const warnLegacyNoGapFill = "this archive predates the config cutover: every setting HEAD filled from the config.yaml beside the database " +
	"migrates empty unless that file is installed beside the restored database before the next start (rerun with --gap-fill <file> to install the copy kept off-host); " +
	"its encrypted rows are read with the host's .encryption_key, which must be in the state directory before the next start"

// checkArchiveFiles refuses --keyring and --gap-fill the way the flags are
// used wrongly: with a checkpoint (which holds its own copies, each checked
// against its recorded SHA-256) or as a relative path.
func checkArchiveFiles(req RestoreOfflineRequest) error {
	if req.Checkpoint != "" && (req.Keyring != "" || req.GapFill != "") {
		return errors.New("kernel: --keyring and --gap-fill go with --archive: a checkpoint holds its own copies of both")
	}
	for _, f := range []struct{ flag, path string }{{"--keyring", req.Keyring}, {"--gap-fill", req.GapFill}} {
		if f.path != "" && !filepath.IsAbs(f.path) {
			return fmt.Errorf("kernel: %s %q is not an absolute path", f.flag, f.path)
		}
	}
	return nil
}

// archiveFile is one operator-supplied file to stage: from is the operator's,
// to the live path it replaces, label its name under staging/state/.
type archiveFile struct {
	label, from, to string
	max             int64
	// private: the file holds the secrets key, so one a group or other can
	// read is refused (secrets export-keyring never writes one).
	private bool
}

// archiveStateFiles is what --keyring and --gap-fill bring back with an
// archive, and the warnings an archive restored without them earns.
func (k *Kernel) archiveStateFiles(archiveID string, req RestoreOfflineRequest) (files []archiveFile, warnings []string) {
	if req.Keyring != "" {
		files = append(files, archiveFile{
			label: keyringLabel, from: req.Keyring, max: maxOperatorFileBytes, private: true,
			to: k.stateFilePath(keyringLabel, filepath.Join(k.cfg.StateDir, keyringFileName)),
		})
	}
	if req.GapFill != "" {
		files = append(files, archiveFile{
			label: gapFillLabel, from: req.GapFill, max: maxOperatorFileBytes,
			to: k.stateFilePath(gapFillLabel, filepath.Join(filepath.Dir(k.cfg.StoragePath), gapFileName)),
		})
	}
	if req.GapFill == "" && backup.IsLegacyID(archiveID) {
		warnings = append(warnings, warnLegacyNoGapFill)
	}
	return files, warnings
}

// stateFilePath is the path the app declared for label, else fallback.
func (k *Kernel) stateFilePath(label, fallback string) string {
	for _, f := range k.cfg.StorageStateFiles {
		if f.Label == label && f.Path != "" {
			return f.Path
		}
	}
	return fallback
}

// copyOperatorFile copies an operator-supplied file into dst, mode 0600,
// synced. The source is opened as a regular file that is not a symbolic link
// (backuparchive.OpenRegular), at most max bytes, and non-empty; a file that
// grows past max while it is read is refused.
func copyOperatorFile(fs vfs.FS, src, dst string, max int64, private bool) (err error) {
	in, fi, err := backuparchive.OpenRegular(src, max)
	if err != nil {
		return fmt.Errorf("kernel: %w", err)
	}
	defer func() { err = errors.Join(err, in.Close()) }()
	if fi.Size() == 0 {
		return fmt.Errorf("kernel: %s is empty", src)
	}
	if private && fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("kernel: %s is readable by group or others (mode %04o) and holds the secrets key: chmod 600 it and rerun", src, fi.Mode().Perm())
	}
	out, err := createPrivate(fs, dst)
	if err != nil {
		return fmt.Errorf("kernel: stage %s: %w", dst, err)
	}
	// A file that was already there keeps the mode it had: make it private before
	// a byte is written (nothing is, on a new one, which was born 0600).
	if err := restorejournal.ChmodFS(fs, dst, 0o600); err != nil {
		return errors.Join(err, out.Close(), removeIfExists(fs, dst))
	}
	n, cerr := io.Copy(out, io.LimitReader(in, max+1))
	if cerr == nil && n > max {
		cerr = fmt.Errorf("%s grew past %d bytes while it was read", src, max)
	}
	if err := errors.Join(cerr, out.Sync(), out.Close()); err != nil {
		return errors.Join(fmt.Errorf("kernel: stage %s: %w", dst, err), removeIfExists(fs, dst))
	}
	d, err := fs.OpenDir(filepath.Dir(dst))
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}

// createPrivate creates dst empty and writable, mode 0600 from the moment it
// exists. On the real filesystem that is os.OpenFile with the mode, then the
// file is opened through fs for writing: vfs.FS has no mode on Create, which
// would leave the file at the umask's mode (0644 usually) until a later chmod,
// and a process that opens it in that window keeps a handle that reads the
// secrets key written next. Any other filesystem (an in-memory one keeps no
// modes) creates it through fs.
func createPrivate(fs vfs.FS, dst string) (vfs.File, error) {
	if vfs.Root(fs) != vfs.Default {
		return fs.Create(dst, vfs.WriteCategoryUnspecified)
	}
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, errors.Join(err, os.Remove(dst))
	}
	out, err := fs.OpenReadWrite(dst, vfs.WriteCategoryUnspecified)
	if err != nil {
		return nil, errors.Join(err, os.Remove(dst))
	}
	return out, nil
}
