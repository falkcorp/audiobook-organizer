// file: internal/opscatalog/testdata/deffixture/internal/synthdefs/defs.go
// version: 1.0.0
// guid: 3c1f6a52-9e0b-4d3a-8f27-5b7c0e1a94d6
// last-edited: 2026-11-08

// Package synthdefs is a link-check fixture that exports ServiceDefs for the
// service registry and defines no bundle: the check must require serviceDefs
// to call it, however else the binary links it.
package synthdefs

import "example.test/fixture/internal/serviceregistry"

func ServiceDefs() []serviceregistry.ServiceDef { return nil }
