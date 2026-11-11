// file: core/auth/loglimit.go
// version: 1.0.0
// guid: a4c12fee-75ca-4f8c-8f89-7a36cdbf1e14
// last-edited: 2026-11-11

package auth

import (
	"sync"
	"time"
)

// logLimiter lets one message per key through per interval and reports how
// many it held back since the last one let through. A warning raised once per
// failing request (an extension whose keys are down, for every request) would
// turn an outage into a log flood; the counter that sits next to it stays
// exact, so nothing is lost but repetition. It is instance state, bounded.
type logLimiter struct {
	mu    sync.Mutex
	every time.Duration
	slots map[string]logSlot
}

type logSlot struct {
	at   time.Time
	held int
}

// maxLogKeys bounds the keys a limiter tracks; past it the oldest slot goes.
const maxLogKeys = 64

// allow reports whether the message for key may be logged now and how many
// were suppressed since the previous one that was.
func (l *logLimiter) allow(key string, now time.Time) (ok bool, held int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.slots == nil {
		l.slots = make(map[string]logSlot)
	}
	s, seen := l.slots[key]
	if seen && now.Sub(s.at) < l.every {
		s.held++
		l.slots[key] = s
		return false, 0
	}
	if !seen && len(l.slots) >= maxLogKeys {
		var oldest string
		var at time.Time
		for k, v := range l.slots {
			if oldest == "" || v.at.Before(at) {
				oldest, at = k, v.at
			}
		}
		delete(l.slots, oldest)
	}
	l.slots[key] = logSlot{at: now}
	return true, s.held
}
