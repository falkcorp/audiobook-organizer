// file: internal/books/appconfig/firstrun.go
// version: 1.0.0
// guid: 3c8a1f57-b2d4-4e69-9a07-5d1e6f8c2b43
// last-edited: 2026-11-19

package appconfig

import (
	"slices"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// firstRunKeys are the settings the first-run wizard writes (web WelcomeWizard
// handleComplete: the library path, the playlist folder, setup_complete, then the
// optional OpenAI key and AI parsing). In the first-run window, when no user
// exists to hold the admin role, an update made only of these keys is allowed
// without it. The list is exact on purpose: every other admin key keeps needing
// the admin role.
var firstRunKeys = []string{"root_dir", "playlist_dir", "setup_complete", "openai_api_key", "enable_ai_parsing"}

// firstRunMay reports whether a's update falls inside the first-run window: the
// actor holds config.FirstRunPerm (the handler grants it only while no user exists
// and setup is not complete) and the update names nothing outside firstRunKeys.
func firstRunMay(a config.Actor, u config.Update) bool {
	if a.Can == nil || !a.Can(config.FirstRunPerm) {
		return false
	}
	for h := range u.Set {
		if !slices.Contains(firstRunKeys, h) {
			return false
		}
	}
	for _, h := range u.Unset {
		if !slices.Contains(firstRunKeys, h) {
			return false
		}
	}
	return true
}

// firstRunActor is the actor core sees for such an update: interactive and holding
// every permission, so core's protection of the keys it writes is met. It is
// built only after firstRunMay has said the update is the wizard's.
func firstRunActor(a config.Actor) config.Actor {
	return config.Actor{Subject: a.Subject, Interactive: true, Owner: false, Can: func(string) bool { return true }}
}
