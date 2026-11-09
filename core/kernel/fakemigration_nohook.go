// file: core/kernel/fakemigration_nohook.go
// version: 1.0.0
// guid: babf86ae-024e-4729-bf36-261a178f11c4
// last-edited: 2026-11-09

//go:build !unitlifecycle

package kernel

// fakeMigrationSteps is the untagged twin of fakemigration_hook.go: no fake
// step exists in any build without the unitlifecycle tag, so no environment
// variable can make a release binary sleep through its boot.
func fakeMigrationSteps() []bootStep { return nil }
