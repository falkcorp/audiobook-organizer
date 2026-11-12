// file: core/auth/oidc/refusals.go
// version: 1.0.0
// guid: 6d1c0e84-2a57-4f93-b8e6-0c9d4a7f1b32
// last-edited: 2026-11-11

package oidc

import (
	"sync"
	"time"
)

// RefusalTTL is how long a refusal that depends on stored state is remembered
// per (provider, subject, address).
//
// The refusals are the two a provider's identity can keep earning on every
// request: not admitted, and an address another account holds that this
// provider may not take. The second costs the global e-mail lock, a scan of
// every user and a denied audit event; with an identity extension at stage 12
// it would be paid on every request of an admitted but refused browser. A
// remembered refusal is answered with no lock, no scan and no audit, and the
// audit event is raised once per TTL.
//
// Only refusals are remembered, never an admission, so a stale entry can only
// delay a sign-in that has become possible (the holder lost its admin role or
// password, the address was added to the allowlist or invited) by at most this
// long; it can never let one through. A refusal is fail-closed, so the bound
// is acceptable, and it is short enough that an operator who fixes the cause
// sees it clear within a minute.
const RefusalTTL = time.Minute

// maxRefusals bounds the remembered refusals. Entries come only from identities
// a provider has already vouched for, so the set is small in practice.
const maxRefusals = 4096

type refusal struct {
	err   error
	until time.Time
}

// refusals is the remembered-refusal set: instance state, bounded, expiring.
type refusals struct {
	mu  sync.Mutex
	m   map[string]refusal
	max int
}

func newRefusals() *refusals { return &refusals{m: map[string]refusal{}, max: maxRefusals} }

// refusalKey is the (provider, subject, address) a refusal is remembered under.
// The address is part of it: a changed address is a new decision.
func refusalKey(provider, subject, email string) string {
	return provider + "\x00" + subject + "\x00" + email
}

// get returns the remembered refusal for key, or nil. An expired entry is gone.
func (c *refusals) get(key string, now time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.m[key]
	if !ok {
		return nil
	}
	if !r.until.After(now) {
		delete(c.m, key)
		return nil
	}
	return r.err
}

// put remembers err for key until now+RefusalTTL. At the bound, expired
// entries go first, then the one expiring soonest.
func (c *refusals) put(key string, err error, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.m[key]; !ok && len(c.m) >= c.max {
		for k, r := range c.m {
			if !r.until.After(now) {
				delete(c.m, k)
			}
		}
		if len(c.m) >= c.max {
			var oldest string
			var at time.Time
			for k, r := range c.m {
				if oldest == "" || r.until.Before(at) {
					oldest, at = k, r.until
				}
			}
			delete(c.m, oldest)
		}
	}
	c.m[key] = refusal{err: err, until: now.Add(RefusalTTL)}
}
