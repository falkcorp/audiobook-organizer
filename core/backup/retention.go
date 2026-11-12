// file: core/backup/retention.go
// version: 1.0.2
// guid: b3a64e29-f8d1-4c07-9e4a-5a0d7c28b1f3
// last-edited: 2026-11-12

package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// restoreRefName is the note StageRestore leaves beside a restore's journal:
// which archive the restore came from and what staging warned, so the archive
// stays floor-protected while the restore is pending, in flight or undo-able
// (floor 2, §5B.3.5), and Pending can report the warnings.
const restoreRefName = "backup-stage.json"

type stageNote struct {
	ArchiveID ArchiveID `json:"archive_id"`
	Foreign   bool      `json:"foreign"`
	Warnings  []string  `json:"warnings,omitempty"`
}

// writeStageNote records what restore id was staged from.
func (s *Svc) writeStageNote(id string, n stageNote) error {
	dir := restorejournal.JournalDir(s.o.StateDir, id)
	if err := mkdirPrivate(dir); err != nil {
		return err
	}
	b, err := json.Marshal(n)
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, restoreRefName), b)
}

func (s *Svc) readStageNote(id string) (stageNote, bool) {
	b, err := readBounded(filepath.Join(restorejournal.JournalDir(s.o.StateDir, id), restoreRefName), 1<<20)
	if err != nil {
		return stageNote{}, false
	}
	var n stageNote
	if json.Unmarshal(b, &n) != nil || n.ArchiveID == "" {
		return stageNote{}, false
	}
	return n, true
}

// restoreRefs is every archive a pending, in-flight or undo-able restore names:
// each restore/<id>/archive.ref whose restore still has its intent, its
// in-flight file or its previous/ directory.
func (s *Svc) restoreRefs() map[ArchiveID]string {
	out := map[ArchiveID]string{}
	root := restorejournal.Dir(s.o.StateDir)
	ents, err := os.ReadDir(root)
	if err != nil {
		return out
	}
	pendingID := ""
	if b, err := readBounded(restorejournal.PendingPath(s.o.StateDir), 1<<20); err == nil {
		var in restorejournal.Intent
		if json.Unmarshal(b, &in) == nil {
			pendingID = in.ID
			if in.ArchiveID != "" {
				out[ArchiveID(in.ArchiveID)] = "a staged restore names it"
			}
		}
	}
	for _, e := range ents {
		if !e.IsDir() || !restorejournal.ValidID(e.Name()) {
			continue
		}
		note, ok := s.readStageNote(e.Name())
		if !ok {
			continue
		}
		live := pendingID == e.Name()
		reason := "a staged restore names it"
		if _, err := os.Lstat(filepath.Join(root, e.Name(), "inflight.json")); err == nil {
			live, reason = true, "a restore in flight names it"
		}
		prev := filepath.Join(filepath.Dir(s.o.DBPath), ".mediacore-restore", e.Name(), "previous")
		if _, err := os.Lstat(prev); err == nil {
			live, reason = true, "an undo point of the restore from it is kept"
		}
		if live {
			out[note.ArchiveID] = reason
		}
	}
	return out
}

// floors is the set of archives retention and Delete must keep, with the
// reason: floor 1, the newest verified archive of this install (an unverified
// newer archive does not protect the last good one); floor 2, any archive a
// pending or undo-able restore references.
//
// Floor 1 considers Own archives only. A Foreign archive reaches the directory
// by import or by hand, and a verification record beside it is a file anyone
// who can write the directory can write: if it could take the floor, a stranger's
// archive (or a forged record) would displace the install's own newest verified
// archive, and retention would then be free to delete it. An archive from before
// provenance (legacy-abo/0, HEAD's own) takes the floor only while no verified
// Own archive exists, which is every install until its first backup under this
// build: pruning all of HEAD's archives before the first new one verifies would
// leave nothing to restore.
func (s *Svc) floors(all []found) map[ArchiveID]string {
	out := s.restoreRefs()
	var own, pre *found
	for i := range all {
		f := &all[i]
		if f.Verified == nil || !f.Verified.OK || f.Verified.Level < VerifyEntries {
			continue
		}
		switch f.Provenance {
		case Own:
			if own == nil || f.CreatedAt.After(own.CreatedAt) {
				own = f
			}
		case PreProvenance:
			if pre == nil || f.CreatedAt.After(pre.CreatedAt) {
				pre = f
			}
		}
	}
	best := own
	if best == nil {
		best = pre
	}
	if best != nil {
		if _, ok := out[best.ID]; !ok {
			out[best.ID] = floorNewestVerified
		}
	}
	return out
}

// floorNewestVerified is floor 1's reason; trimJournal compares against it.
const floorNewestVerified = "it is the newest verified archive"

// Prune is the prune task's body: the retention bounds with their floors.
func (s *Svc) Prune(ctx context.Context) error {
	release, err := s.acquire()
	if err != nil {
		return err
	}
	defer release()
	return s.prune(ctx, s.o.settings())
}

// prune applies the count, age and byte bounds oldest first, never deleting a
// floor-protected archive, never crediting a failed delete (§5B.3.5).
func (s *Svc) prune(ctx context.Context, set Settings) error {
	all, err := s.scan(ctx)
	if err != nil {
		return err
	}
	prot := s.floors(all)
	oldest := append([]found(nil), all...)
	sort.Slice(oldest, func(i, j int) bool { return oldest[i].CreatedAt.Before(oldest[j].CreatedAt) })
	count, total := len(oldest), int64(0)
	for _, f := range oldest {
		total += f.Bytes
	}
	now := s.o.now()
	for _, f := range oldest {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, keep := prot[f.ID]; keep {
			continue
		}
		over := count > set.MaxCount ||
			(set.MaxAge > 0 && now.Sub(f.CreatedAt) > set.MaxAge) ||
			(set.MaxTotalBytes > 0 && total > set.MaxTotalBytes)
		if !over {
			continue
		}
		if err := s.deleteFiles(f); err != nil {
			s.o.log().Warn("backup: retention could not delete an archive", "archive", f.Name, "error", err)
			continue
		}
		count--
		total -= f.Bytes
	}
	return s.trimJournal(ctx, prot)
}

func (s *Svc) deleteFiles(f found) error {
	var first error
	for _, suf := range []string{"", ChecksumSidecar, ManifestSidecar, VerifySidecar} {
		if err := os.Remove(f.Path + suf); err != nil && !os.IsNotExist(err) && first == nil {
			first = err
		}
	}
	s.cache.Delete(f.Path)
	return first
}

// trimJournal drops the revocation journal's lines no retained archive needs.
// It does nothing while a pending or undo-able restore exists, whose boundary
// this package does not hold, or when no retained archive records one.
func (s *Svc) trimJournal(ctx context.Context, prot map[ArchiveID]string) error {
	if s.o.PruneJournal == nil {
		return nil
	}
	for id, why := range prot {
		if why != floorNewestVerified {
			s.o.log().Debug("backup: journal not trimmed", "archive", id, "reason", why)
			return nil
		}
	}
	remaining, err := s.scan(ctx)
	if err != nil {
		return err
	}
	var min uint64
	have := false
	for _, f := range remaining {
		if f.Manifest == nil {
			continue
		}
		if !have || f.Manifest.JournalSeq < min {
			min, have = f.Manifest.JournalSeq, true
		}
	}
	if !have {
		return nil
	}
	if err := s.o.PruneJournal(ctx, min); err != nil {
		return fmt.Errorf("backup: trim the revocation journal: %w", err)
	}
	return nil
}

// Ensure is "archive unless one verified within maxAge" (organizer's
// pre-organize backup): a verified archive no older than maxAge is returned as
// is; otherwise a new one is made with reason.
//
// The reason is judged first, whether or not it will be used: a caller whose
// reason Create would refuse is told so now, not on the day no recent archive
// exists (an empty reason is legal, and Create defaults it).
func (s *Svc) Ensure(ctx context.Context, maxAge time.Duration, reason string) (Archive, bool, error) {
	if err := ValidReason(reason); err != nil {
		return Archive{}, false, err
	}
	all, err := s.scan(ctx)
	if err != nil {
		return Archive{}, false, err
	}
	now := s.o.now()
	for _, f := range all { // newest first
		if f.Verified != nil && f.Verified.OK && f.Verified.Level >= VerifyEntries && f.Provenance == Own && now.Sub(f.CreatedAt) <= maxAge {
			return f.Archive, false, nil
		}
	}
	a, err := s.Create(ctx, CreateOptions{Reason: reason})
	return a, err == nil, err
}
