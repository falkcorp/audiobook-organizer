// file: internal/server/handlers/user_credentials.go
// version: 1.0.0
// guid: dfbcd913-98ce-4d11-bcc4-3eb6d9cbdf7d
// last-edited: 2026-10-19

package handlers

import "github.com/falkcorp/audiobook-organizer/internal/database"

// ResolveUserCredentialWriter finds database.UserCredentialWriter (the
// field-scoped password, status and credential-epoch ops) through the store's
// decorator chain. It is a capability rather than a Store method because the
// Store width baseline may only go down.
//
// It returns nil when the store lacks it, and every caller then refuses the
// change (500) rather than fall back to a whole-row UpdateUser: a credential or
// status change that did not bump CredentialEpoch would leave every ABS session
// of the user valid.
func ResolveUserCredentialWriter(store any) database.UserCredentialWriter {
	w, ok := database.AsCapability[database.UserCredentialWriter](store)
	if !ok {
		return nil
	}
	return w
}
