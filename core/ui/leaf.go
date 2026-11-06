// file: core/ui/leaf.go
// version: 1.0.0
// guid: b1325c94-e130-487d-a1a1-f928cc9aba2a
// last-edited: 2026-11-06

// Package ui is the Go side of core's UI shell (07 §7.6). This file is the
// dependency-free leaf of the package that lands in release R1: the function
// ReservedSegments and the plain types ModuleManifest and UIIDs. ReservedSegments
// lets module.ReservedIDs() hold from R1, and the two types give
// internal/modulehttp (L6, R2's identity flip) a type for modules[].ui. The
// rest of the package, WriteManifestTS included, lands with the shell work
// (FE3-8, FE4-6). It imports nothing, the standard library included.
package ui

// ReservedSegments lists the first segment of every shell route (§7.4.3);
// module.ReservedIDs() includes them. Returns a fresh slice on every call
// (R5: no exported slice variable). It and the plain types ModuleManifest,
// UIIDs below form the dependency-free leaf of package ui that
// lands in R1, so internal/modulehttp (L6, R2's identity flip) has a type for
// modules[].ui; the rest of the package lands with the shell work (FE3-8,
// FE4-6).
func ReservedSegments() []string {
	return []string{"activity", "invite", "login", "settings", "setup", "system", "users"}
}

// ModuleManifest is one module's UI manifest: what /api/core/v1/modules
// reports and what manifest.gen.ts prints (§7.4.1). It lives in ui (L6), not
// in app (L9), so ui never imports app (R4); app.Description.UIManifests is a
// []ui.ModuleManifest (COR2-18). R1 leaf type (FE4-6).
type ModuleManifest struct {
	ID          string
	Version     string
	DisplayName string
	UI          UIIDs    // the module.UIDescriptor lists, copied by the kernel
	Permissions []string // the module's permission names
}

// UIIDs mirrors module.UIDescriptor (06 §6.4.2) field by field.
type UIIDs struct {
	Pages, Nav, SettingsPanels, Widgets, Banners, TopBarItems,
	UserPage, ActivityPanes, SetupSteps []string
}
