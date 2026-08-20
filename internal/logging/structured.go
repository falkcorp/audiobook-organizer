// file: internal/logging/structured.go
// version: 1.1.1

package logging

import (
	"context"
	"encoding/json"
	"log/slog"
)

// opAttrs builds a list of slog attributes from an OpContext.
// Returns empty list if op is nil.
func opAttrs(op *OpContext) []any {
	if op == nil {
		return []any{}
	}
	attrs := []any{
		"opID", op.ID,
		"opType", op.Type,
		"opStatus", op.Status,
	}
	if len(op.Entities) > 0 {
		entitiesJSON, _ := json.Marshal(op.Entities)
		attrs = append(attrs, "entities", string(entitiesJSON))
	}
	return attrs
}

// sanitizeArgs escapes CR/LF in string-typed attribute VALUES (odd positions
// in slog's alternating key,value convention). Keys are program constants and
// are left alone; non-string values (ints, errors passed as any, slog.Attr)
// are left alone — callers logging tainted error text should pass
// SanitizeErr(err) explicitly. This makes Info/Warn/Error/Debug below a
// sanitizing barrier for every caller in one place (CA12).
func sanitizeArgs(args []any) []any {
	for i := 1; i < len(args); i += 2 {
		if s, ok := args[i].(string); ok {
			args[i] = Sanitize(s)
		}
	}
	return args
}

// Info logs a message with operation context from ctx automatically included.
// Additional attrs are appended after operation attributes.
func Info(ctx context.Context, msg string, attrs ...any) {
	op := OpFromContext(ctx)
	args := sanitizeArgs(append(opAttrs(op), attrs...))
	slog.Info(Sanitize(msg), args...)
}

// Warn logs a warning with operation context from ctx automatically included.
func Warn(ctx context.Context, msg string, attrs ...any) {
	op := OpFromContext(ctx)
	args := sanitizeArgs(append(opAttrs(op), attrs...))
	slog.Warn(Sanitize(msg), args...)
}

// Error logs an error with operation context from ctx automatically included.
func Error(ctx context.Context, msg string, attrs ...any) {
	op := OpFromContext(ctx)
	args := sanitizeArgs(append(opAttrs(op), attrs...))
	slog.Error(Sanitize(msg), args...)
}

// Debug logs a debug message with operation context from ctx automatically included.
func Debug(ctx context.Context, msg string, attrs ...any) {
	op := OpFromContext(ctx)
	args := sanitizeArgs(append(opAttrs(op), attrs...))
	slog.Debug(Sanitize(msg), args...)
}
