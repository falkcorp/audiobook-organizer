// file: internal/logger/operation.go
// version: 1.4.0
// guid: 7b3f9c1a-4e2d-4a8b-9c5e-1d2f3a4b5c6d
// last-edited: 2026-06-17

package logger

import (
	"fmt"
	"sync"
	"sync/atomic"
)

// OperationStore is the subset of database.Store needed by OperationLogger.
type OperationStore interface {
	AddOperationLog(operationID, level, message string, details *string) error
	CreateOperationChange(change interface{}) error
	UpdateOperationProgress(id string, current, total int, message string) error
}

// RealtimeHub is the subset of the real-time hub needed by OperationLogger.
type RealtimeHub interface {
	SendOperationLog(operationID, level, message string, details *string)
	SendOperationProgress(operationID string, current, total int, message string)
}

// sharedState holds data shared between a parent OperationLogger and its children.
type sharedState struct {
	mu       sync.Mutex
	changes  []Change
	counters map[string]int
	canceled *atomic.Bool
}

// OperationLogger logs to stdout + operation DB + real-time hub.
type OperationLogger struct {
	operationID string
	subsystem   string
	store       OperationStore
	hub         RealtimeHub
	minDBLevel  Level
	minStdout   Level
	shared      *sharedState
}

// ForOperation creates an OperationLogger bound to a specific operation.
func ForOperation(operationID string, store OperationStore, hub RealtimeHub) *OperationLogger {
	return &OperationLogger{
		operationID: operationID,
		store:       store,
		hub:         hub,
		minDBLevel:  LevelInfo,
		minStdout:   LevelDebug,
		shared: &sharedState{
			counters: make(map[string]int),
			canceled: &atomic.Bool{},
		},
	}
}

// SetMinDBLevel sets the minimum level for DB/real-time logging.
func (l *OperationLogger) SetMinDBLevel(level Level) {
	l.minDBLevel = level
}

// SetCanceled marks the operation as canceled.
func (l *OperationLogger) SetCanceled() {
	l.shared.canceled.Store(true)
}

func (l *OperationLogger) log(level Level, msg string, args ...any) {
	formatted := sanitizeLogLine(fmt.Sprintf(msg, args...))

	if level >= l.minStdout {
		logToStdout(l.subsystem, level, msg, args...)
	}

	if level >= l.minDBLevel {
		if l.store != nil {
			_ = l.store.AddOperationLog(l.operationID, level.String(), formatted, nil)
		}
		if l.hub != nil {
			l.hub.SendOperationLog(l.operationID, level.String(), formatted, nil)
		}
	}
}

func (l *OperationLogger) Trace(msg string, args ...any) { l.log(LevelTrace, msg, args...) }
func (l *OperationLogger) Debug(msg string, args ...any) { l.log(LevelDebug, msg, args...) }
func (l *OperationLogger) Info(msg string, args ...any)  { l.log(LevelInfo, msg, args...) }
func (l *OperationLogger) Warn(msg string, args ...any)  { l.log(LevelWarn, msg, args...) }
func (l *OperationLogger) Error(msg string, args ...any) { l.log(LevelError, msg, args...) }

// UpdateProgress sends progress to the store and hub.
func (l *OperationLogger) UpdateProgress(current, total int, message string) {
	if l.store != nil {
		_ = l.store.UpdateOperationProgress(l.operationID, current, total, message)
	}
	if l.hub != nil {
		l.hub.SendOperationProgress(l.operationID, current, total, message)
	}
}

// RecordChange appends a change to the shared slice and increments the counter.
func (l *OperationLogger) RecordChange(change Change) {
	l.shared.mu.Lock()
	defer l.shared.mu.Unlock()
	l.shared.changes = append(l.shared.changes, change)
	l.shared.counters[change.ChangeType]++
}

// ChangeCounters returns a copy of the change-type counters.
func (l *OperationLogger) ChangeCounters() map[string]int {
	l.shared.mu.Lock()
	defer l.shared.mu.Unlock()
	cp := make(map[string]int, len(l.shared.counters))
	for k, v := range l.shared.counters {
		cp[k] = v
	}
	return cp
}

// Changes returns a copy of all recorded changes.
func (l *OperationLogger) Changes() []Change {
	l.shared.mu.Lock()
	defer l.shared.mu.Unlock()
	cp := make([]Change, len(l.shared.changes))
	copy(cp, l.shared.changes)
	return cp
}

// IsCanceled reports whether the operation has been marked canceled.
func (l *OperationLogger) IsCanceled() bool {
	return l.shared.canceled.Load()
}

// OperationID returns the operation ID this logger is bound to.
func (l *OperationLogger) OperationID() string {
	return l.operationID
}

// With returns a child Logger that shares state with the parent.
func (l *OperationLogger) With(subsystem string) Logger {
	prefix := subsystem
	if l.subsystem != "" {
		prefix = l.subsystem + "." + subsystem
	}
	return &OperationLogger{
		operationID: l.operationID,
		subsystem:   prefix,
		store:       l.store,
		hub:         l.hub,
		minDBLevel:  l.minDBLevel,
		minStdout:   l.minStdout,
		shared:      l.shared, // shared state
	}
}

// Log implements the ProgressReporter.Log interface for backward compatibility.
func (l *OperationLogger) Log(level, message string, details *string) error {
	lvl := ParseLevel(level)
	message = sanitizeLogLine(message)
	if lvl >= l.minStdout {
		logToStdout(l.subsystem, lvl, "%s", message)
	}
	if lvl >= l.minDBLevel {
		if l.store != nil {
			_ = l.store.AddOperationLog(l.operationID, level, message, details)
		}
		if l.hub != nil {
			l.hub.SendOperationLog(l.operationID, level, message, details)
		}
	}
	return nil
}
