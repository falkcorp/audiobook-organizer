// file: core/rbac/export_test.go
// version: 1.0.0
// guid: 1f6c3b82-9d4e-4a57-b8c0-2e7a5d91f3c4
// last-edited: 2026-11-10

package rbac

// SetBeforeCommit installs the hook a role write runs after staging and before
// its Commit (the concurrency tests' seam). Call it while no write runs.
func SetBeforeCommit(s *Service, f func()) { s.beforeCommit = f }
