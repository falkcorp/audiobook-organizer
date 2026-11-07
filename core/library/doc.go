// file: core/library/doc.go
// version: 1.0.0
// guid: f270ac67-f593-4ab0-a2f5-42435176815e
// last-edited: 2026-11-07

// Package library holds the path-safety leaves of the media core: safe path
// joining and validation (SafeJoin, WithinRoot, SecureJoin, SecureJoinResolved,
// CleanAbsolutePath, SanitizeFilename, IsDangerousRoot, SafePath), the library
// root checks (Mounted, ReportPathNotify, EnsureDirWritable), the bounded-call
// helper (Call), disk statistics (Stats), and the standard permission modes
// (DirMode, FileMode, SecretFileMode). It imports only the standard library.
package library
