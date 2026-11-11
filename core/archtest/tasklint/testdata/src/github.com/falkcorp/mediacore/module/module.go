// file: core/archtest/tasklint/testdata/src/github.com/falkcorp/mediacore/module/module.go
// version: 1.0.0
// guid: e54fad2a-4a1d-444b-ab0a-75e902f993e3
// last-edited: 2026-11-10

// Package module is a minimal stub of core/module for the tasklint fixtures:
// Registrar.Tasks is the one registration call (06 §6.4.3).
package module

import "github.com/falkcorp/mediacore/tasks"

type Registrar struct{}

func (r *Registrar) Tasks(defs ...tasks.Definition) {}
