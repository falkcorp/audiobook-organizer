// file: core/secrets/derive.go
// version: 1.0.0
// guid: 1395a628-efb2-4f2f-a681-3a1021cc8a56
// last-edited: 2026-11-10

package secrets

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// checkInfo refuses an empty info and the store's own subkey namespace.
func checkInfo(info string) error {
	switch {
	case info == "":
		return errors.New("secrets: DeriveKey needs an info string")
	case strings.HasPrefix(info, reservedInfoPrefix):
		return fmt.Errorf("secrets: info strings under %q are the store's own subkeys", reservedInfoPrefix)
	}
	return nil
}

// DeriveKey derives a 32-byte key for info from the active derive key
// (HKDF-SHA256). It is stable across data-key rotation; producers record
// ActiveDeriveID beside what they MAC or encrypt.
func (s *Store) DeriveKey(info string) ([]byte, error) {
	if err := checkInfo(info); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, errClosed
	}
	return subkey(s.ring.derive.Material, info)
}

// ActiveDeriveID is the active derive key's ID ("d1"); "" after Close.
func (s *Store) ActiveDeriveID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return ""
	}
	return s.ring.derive.ID
}

// DeriveKeyFor derives info's key from the derive key kid, which must be
// active or in verify status (and not retired by RetireUnused):
// ErrKeyUnavailable for a retired, unknown or data key. It verifies and
// decrypts what was produced under an older derive key.
func (s *Store) DeriveKeyFor(kid, info string) ([]byte, error) {
	if err := checkInfo(info); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, errClosed
	}
	k, ok := s.ring.byID[kid]
	if !ok || k.Use != useDerive {
		return nil, fmt.Errorf("%w: no derive key %q", ErrKeyUnavailable, kid)
	}
	switch {
	case k.Status == statusActive:
	case k.Status == statusVerify && !s.retired[kid]:
	default:
		return nil, fmt.Errorf("%w: derive key %q is retired", ErrKeyUnavailable, kid)
	}
	return subkey(k.Material, info)
}

// RetireUnused treats as retired, for this process, every verify-status
// derive key past its retire_after that inUse reports unused, and returns
// their IDs. A key inUse reports used, or for which it returns an error, is
// kept and listed by RetireBlocked (05-health derive_key_retire_blocked):
// fail-safe, never retired under something that may need it. In memory only:
// the keyring file keeps "verify" and the decision is remade every boot. The
// kernel calls it in 06 P5 with the backup service's check (05-backup). Only
// a nil inUse or a cancelled ctx is an error.
func (s *Store) RetireUnused(ctx context.Context, inUse func(ctx context.Context, kid string) (bool, error)) (retired []string, err error) {
	if inUse == nil {
		return nil, errors.New("secrets: RetireUnused needs an in-use check")
	}
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return nil, errClosed
	}
	now := s.now()
	var due []string
	for _, k := range s.ring.file.Keys {
		if k.Use == useDerive && k.Status == statusVerify && !s.retired[k.ID] && now.After(*k.RetireAfter) {
			due = append(due, k.ID)
		}
	}
	s.mu.RUnlock()

	// The check reads the backup directory and the journal: it runs with no
	// lock held.
	var blocked []string
	for _, kid := range due {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		used, cerr := inUse(ctx, kid)
		switch {
		case cerr != nil:
			s.log.Warn("secrets: a derive key past retire_after is kept: the in-use check failed", "kid", kid, "error", cerr)
			blocked = append(blocked, kid)
		case used:
			s.log.Info("secrets: a derive key past retire_after is kept: something retained still names it", "kid", kid)
			blocked = append(blocked, kid)
		default:
			retired = append(retired, kid)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errClosed
	}
	for _, kid := range retired {
		s.retired[kid] = true
	}
	s.blocked = blocked
	return retired, nil
}

// RetireBlocked is the verify derive keys the last RetireUnused kept past
// their retire_after (a name coined by F3-03, for 05-health
// derive_key_retire_blocked).
func (s *Store) RetireBlocked() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.blocked)
}
