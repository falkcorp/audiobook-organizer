// file: core/secrets/errors.go
// version: 1.0.0
// guid: 2c771d60-b0df-45a6-bc63-fe3aa420dd18
// last-edited: 2026-11-10

package secrets

import "errors"

// The store's sentinel errors (05-config §2.3, §2.5). ErrUnbound and
// ErrNotSerializable are declared beside Ref and Value in types.go.
var (
	// ErrNotSet: the secret has no value: no record, a cleared marker, and
	// no operator value.
	ErrNotSet = errors.New("secrets: not set")
	// ErrKeyUnavailable: the record names a key the keyring does not hold
	// (or holds retired), or carries the cutover's "key" unreadable marker.
	// The secret must be re-entered; putting the old keyring back recovers
	// it.
	ErrKeyUnavailable = errors.New("secrets: encryption key for this value is not in the keyring")
	// ErrCorrupt: the record failed authentication (tampering, or a
	// ciphertext moved to another name or app), does not decode, or carries
	// the cutover's "corrupt" unreadable marker. It is never repaired
	// automatically.
	ErrCorrupt = errors.New("secrets: ciphertext failed authentication")
	// ErrKeyfileInsecure: the keyring file is readable or writable by group
	// or others and could not be set back to 0600.
	ErrKeyfileInsecure = errors.New("secrets: keyring file permissions are too open")
	// ErrKeyMissing: there is no keyring file and the store holds encrypted
	// records; Open refuses unless AllowKeyRegen.
	ErrKeyMissing = errors.New("secrets: no keyring and encrypted values exist")
	// ErrOutOfScope: a Scope was asked for a name outside its prefix.
	ErrOutOfScope = errors.New("secrets: name outside this handle's scope")
	// ErrAppIDMismatch: c/secrets/meta names another app ID than
	// Options.AppID, so every record's AAD would fail.
	ErrAppIDMismatch = errors.New("secrets: store belongs to a different app ID")
)

// errClosed: a method ran after Close.
var errClosed = errors.New("secrets: the store is closed")
