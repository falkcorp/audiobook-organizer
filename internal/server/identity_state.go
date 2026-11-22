// file: internal/server/identity_state.go
// version: 0.1.0
// guid: 1b6d3f84-9a52-4c07-8e1d-0f4a7c25b963
// last-edited: 2026-11-21

package server

// UnderCoreIdentity reports whether the server was built to run under core
// identity (WithIdentity), the same value every adopted site reads (s.identity).
// Books reads it at Attach to hold the server, the identity predicate and the
// store's level together.
func (s *Server) UnderCoreIdentity() bool { return s.identity != nil }
