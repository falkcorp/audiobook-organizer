// file: internal/server/quarantine_known_bad.go
// version: 1.2.0
// guid: d4e5f6a7-b8c9-0d1e-2f3a-4b5c6d7e8f9a
// last-edited: 2026-07-07

package server

import (
	"log/slog"

	"github.com/falkcorp/audiobook-organizer/internal/remux"
)

const quarantineKnownBadKey = "quarantine_known_bad_v1_done"

// quarantineKnownBadFiles runs once at startup: finds any book whose file path
// is marked as permanently taglib-unreadable (transcode_skip_* setting = "true")
// and quarantines it. These are the ~29 files the transcode pass could not fix.
func (s *Server) quarantineKnownBadFiles() {
	store := s.Store()
	if store == nil {
		return
	}

	if setting, err := store.GetSetting(quarantineKnownBadKey); err == nil && setting != nil && setting.Value == "true" {
		return
	}

	books, err := store.GetAllBooksCore(20000, 0)
	if err != nil {
		slog.Warn("quarantineKnownBadFiles GetAllBooksCore", "err", err)
		return
	}

	quarantined := 0
	for _, b := range books {
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
	_ = store.SetSetting(quarantineKnownBadKey, "true", "bool", false)
}
