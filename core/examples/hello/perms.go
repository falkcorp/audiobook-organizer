// file: core/examples/hello/perms.go
// version: 1.0.0
// guid: a7c598d6-d584-461b-8d04-57b8bc166f4f
// last-edited: 2026-11-11

package hello

import "github.com/falkcorp/mediacore/rbac"

// PermAdmin is hello's maintenance scope, the Perms.Start of hello.count (06
// §6.12.1, §6.12.3). It is declared apart from hello.go because the whole
// permission set (PermRead, PermSettings and Manifest.Permissions) lands with
// the row that lands module.Manifest.Permissions; that row moves this
// constant into hello.go with its siblings. Nothing registers the scope in
// the catalog yet, and hello.count is not registered either (F2-16).
const PermAdmin rbac.Permission = "hello:admin"
