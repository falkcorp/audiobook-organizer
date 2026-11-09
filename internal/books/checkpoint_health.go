// file: internal/books/checkpoint_health.go
// version: 1.0.0
// guid: ed7b4a70-f638-4d9d-829a-8461221d5063
// last-edited: 2026-11-08

package books

import (
	"fmt"
	"time"

	system "github.com/falkcorp/audiobook-organizer/internal/server/handlers/system"
	"github.com/falkcorp/mediacore/storage"
)

// checkpointPosture turns the App's checkpoint health into the posture
// tokens GET /system/status reports (05-storage §5S.3.12; 05-health
// §5H.3.1, §5H.10 R1), never /health:
//
//   - storage_checkpoint_retained for every checkpoint, with its
//     delete_after (none yet while it is the live store's rollback);
//   - storage_checkpoint_open for an open checkpoint that is still the
//     rollback, and for every scratch entry, with its age;
//   - storage_checkpoint_mode for every group- or world-accessible path the
//     last sweep found, and one entry counting the paths it did not list.
//
// A health read that fails raises storage_checkpoint_retained without a
// subject's name: the copy holds plaintext secrets, so a checkpoint that
// cannot be listed is reported rather than left out.
func checkpointPosture(h storage.CheckpointHealth, err error, now time.Time) []system.PostureToken {
	if err != nil {
		bootLog.Warn("checkpoint health unreadable; reporting storage_checkpoint_retained without its subject: %v", err)
		return []system.PostureToken{{Token: system.DegradedStorageCheckpointRetained, Subject: "the checkpoint directory could not be read"}}
	}
	age := func(t time.Time) int64 { return max(0, int64(now.Sub(t)/time.Second)) }
	var out []system.PostureToken
	for _, c := range h.Checkpoints {
		out = append(out, system.PostureToken{Token: system.DegradedStorageCheckpointRetained, Subject: c.Name, DeleteAfter: c.DeleteAfter})
		if c.Rollback {
			out = append(out, system.PostureToken{Token: system.DegradedStorageCheckpointOpen, Subject: c.Name, AgeSeconds: age(c.TakenAt)})
		}
	}
	for _, s := range h.Scratch {
		out = append(out, system.PostureToken{Token: system.DegradedStorageCheckpointOpen, Subject: s.Path, AgeSeconds: age(s.ModTime)})
	}
	for _, p := range h.Loose {
		out = append(out, system.PostureToken{Token: system.DegradedStorageCheckpointMode, Subject: p})
	}
	if more := h.LooseTotal - len(h.Loose); more > 0 {
		out = append(out, system.PostureToken{Token: system.DegradedStorageCheckpointMode, Subject: fmt.Sprintf("%d more paths not listed", more)})
	}
	return out
}
