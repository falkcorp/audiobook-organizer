// file: core/kernel/logconfig.go
// version: 0.1.0
// guid: 7c41d0b9-2e65-4a83-b1f7-9d3a5e0c6b24
// last-edited: 2026-11-21

package kernel

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/log"
)

// Live logging config (05-logging §4 item 3). The kernel subscribes to the
// core.log handle: level, levels, add_source and redact_keys apply to the
// running pipeline when config commits a change; format, output and file are
// restart-bound (their tags are reload:"restart", so config never publishes a
// changed effective value for them and the subscriber is never woken for one).
//
// Nothing in boot calls watchLogConfig yet, because boot does not build the
// layered config store yet (config.go: bootConfig is dark until F3-07). The
// F3-07 flip calls it once, with the handles of the store it loaded, after
// that store has read its layers; the subscription ends at Store.Close, so
// closeCore needs no stop for it.

// logHandle finds the core.log handle among a config boot's handles.
func logHandle(handles []any) (*config.Handle[log.Settings], bool) {
	for _, h := range handles {
		if lh, ok := h.(*config.Handle[log.Settings]); ok && lh.Path() == logSection {
			return lh, true
		}
	}
	return nil, false
}

// logSection is core.log's path.
const logSection config.Path = "core.log"

// watchLogConfig subscribes applyLogSettings to the core.log handle among
// handles and returns the subscription's stop (idempotent; it waits for an
// in-flight apply and must not be called from the subscriber). It is an error
// that handles has no core.log handle: a boot that cannot apply a change live
// must say so, not run with the setting silently boot-only.
func (k *Kernel) watchLogConfig(handles []any) (stop func(), err error) {
	h, ok := logHandle(handles)
	if !ok {
		return nil, fmt.Errorf("kernel: logging: no %s handle among the config handles", logSection)
	}
	l := k.coreLog("logconfig")
	// cur is the state the pipeline is in: what boot built it from, then what
	// each accepted change made it. Only the subscriber's own goroutine
	// touches it.
	cur := k.bootLogSettings()
	return h.OnChange(func(_ context.Context, prev, next *log.Settings) {
		want := overlayLogChange(cur, *prev, *next)
		// A refusal is reported inside; the subscriber has nothing to return
		// to, and the old state stands.
		if k.applyLogSettings(l, want) == nil {
			cur = want
		}
	}), nil
}

// bootLogSettings is the core.log section buildLogging builds the pipeline
// from: Config.Log when the app supplies one, otherwise the defaults.
func (k *Kernel) bootLogSettings() log.Settings {
	if k.cfg.Log != nil {
		return *k.cfg.Log
	}
	return log.DefaultSettings()
}

// overlayLogChange is cur with the live keys that differ between prev and
// next replaced by next's. The store's snapshot is the whole section, but the
// pipeline may have been built from Config.Log, which the store does not
// hold (until F3-07 slice (e) reads core.log from the layers): applying next
// wholesale would turn a change to one key into a reset of the others to the
// store's values, for example an operator adding a redact_keys entry would
// put the level back to the section's default.
func overlayLogChange(cur, prev, next log.Settings) log.Settings {
	out := cur
	if next.Level != prev.Level {
		out.Level = next.Level
	}
	if !maps.Equal(next.Levels, prev.Levels) {
		out.Levels = next.Levels
	}
	if next.AddSource != prev.AddSource {
		out.AddSource = next.AddSource
	}
	if !slices.Equal(next.RedactKeys, prev.RedactKeys) {
		out.RedactKeys = next.RedactKeys
	}
	return out
}

// applyLogSettings applies the live keys of s to the running pipeline. Config
// validates a section before it commits it (log.Settings.Validate), so a bad
// value should never get here; if one does, nothing is applied (the pipeline
// parses everything before it changes anything), the previous state stands
// and the refusal is logged at error level and returned.
//
// With an adopted Config.Logger there is no pipeline (k.logging is nil) and
// levels and add_source belong to that logger; redact_keys still applies,
// because the Redactor is the kernel's own and the metrics and HTTP layers
// share it.
func (k *Kernel) applyLogSettings(l *slog.Logger, s log.Settings) error {
	if k.logging == nil {
		if err := s.Validate(); err != nil {
			l.Error("core.log change refused; keeping the previous redaction", "error", err)
			return err
		}
		k.redactor.SetExtraKeys(s.RedactKeys)
		return nil
	}
	unknown, err := k.logging.ApplySettings(s)
	if err != nil {
		l.Error("core.log change refused; keeping the previous logging state", "error", err)
		return err
	}
	if len(unknown) > 0 {
		// Fail-open (05-logging §6): the entry stays in the table for a scope
		// created later.
		l.Warn("core.log.levels names scopes no logger uses yet", "scopes", unknown)
	}
	l.Info("core.log applied", "level", s.Level, "add_source", s.AddSource, "levels", len(s.Levels), "redact_keys", len(s.RedactKeys))
	return nil
}
