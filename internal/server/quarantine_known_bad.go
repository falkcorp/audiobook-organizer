// file: internal/server/quarantine_known_bad.go
// version: 1.3.0
// guid: d4e5f6a7-b8c9-0d1e-2f3a-4b5c6d7e8f9a
// last-edited: 2026-10-15

package server

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/remux"
)

const quarantineKnownBadKey = "quarantine_known_bad_v1_done"

var quarantineLog = logger.New("quarantine-known-bad")

// quarantineKnownBadFiles finds any book whose file path is marked as
// permanently taglib-unreadable (transcode_skip_* setting = "true") and
// quarantines it. These are the ~29 files the transcode pass could not fix.
// It runs once (gated by quarantineKnownBadKey), as the second step of
// maintenance.malformed-m4b-transcode when that op is enqueued with
// {"quarantine_after": true}, which boot does.
//
// ctx is the op's run context. It is checked per book, and a cancelled run
// returns ctx's error without setting the done flag, so the registry's
// shutdown drain (which cancels ctx before the store closes) is not left
// waiting on a whole-library walk, and the next run starts over.
// alreadyDone is true when the flag was set before the call.
func (s *Server) quarantineKnownBadFiles(ctx context.Context) (quarantined int, alreadyDone bool, err error) {
	store := s.Ops()
	if store == nil {
		return 0, false, nil
	}

	if setting, err := store.GetSetting(quarantineKnownBadKey); err == nil && setting != nil && setting.Value == "true" {
		return 0, true, nil
	}
	if err := ctx.Err(); err != nil {
		return 0, false, err
	}

	books, err := store.GetAllBooksCore(0, 0)
	if err != nil {
		return 0, false, fmt.Errorf("quarantine known-bad files: list books: %w", err)
	}

	for _, b := range books {
		if err := ctx.Err(); err != nil {
			quarantineLog.Info("quarantine of known-bad files interrupted after %d book(s); the done flag stays unset", quarantined)
			return quarantined, false, err
		}
		if b.QuarantinedAt != nil {
			continue
		}
		key := remux.TranscodeSkipKey(b.FilePath)
		setting, err := store.GetSetting(key)
		if err != nil || setting == nil || setting.Value != "true" {
			continue
		}
		if err := s.quarantineSvc.QuarantineBook(b.ID, "taglib permanently unreadable after transcode attempt"); err != nil {
			slog.Warn("quarantineKnownBadFiles quarantine", "b", b.FilePath, "err", err)
			continue
		}
		slog.Info("quarantineKnownBadFiles quarantined", "b", b.FilePath)
		quarantined++
	}

	slog.Info("quarantineKnownBadFiles quarantined books", "quarantined", quarantined)
	if err := store.SetSetting(quarantineKnownBadKey, "true", "bool", false); err != nil {
		return quarantined, false, fmt.Errorf("quarantine known-bad files: set done flag: %w", err)
	}
	return quarantined, false, nil
}
