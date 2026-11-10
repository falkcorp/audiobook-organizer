// file: core/config/options.go
// version: 1.0.0
// guid: 8e9c571c-9236-40b5-8f6f-60cdc5d93911
// last-edited: 2026-11-10

package config

import (
	"encoding/json"
	"maps"
	"slices"
)

// Reload says when a change takes effect.
type Reload uint8

const (
	// ReloadLive is published to Get() and OnChange on Apply.
	ReloadLive Reload = iota + 1
	// ReloadRestart is persisted on Apply; Get() keeps the boot value until restart.
	ReloadRestart
	// ReloadBoot is read before storage opens (05-config §1.3.5 boot record),
	// persisted outside the DB, restart required. Used for decisions taken
	// before module registration, e.g. core.modules.<id>.enabled (06, COR-14).
	ReloadBoot
)

// Protection says who may change a key through Apply (the DB layer or the boot
// record). Ordered: each level includes the requirements of the levels above it
// in this list.
type Protection uint8

const (
	// ProtectNone: the section's write permission suffices.
	ProtectNone Protection = iota + 1
	// ProtectInteractive also needs an interactive, credential-changing session (05-authn).
	ProtectInteractive
	// ProtectAdmin also needs core:admin (SEC-6): sign-in, proxy, updater, path, URL keys.
	ProtectAdmin
	// ProtectOwner also needs the owner proof (05-authn) while an owner identity
	// is set and an owner verifier is configured; until then it is checked as ProtectAdmin.
	ProtectOwner
	// ProtectOperator is never writable via Apply: file, env or flag only.
	ProtectOperator
)

// envBinding is one EnvBinding option.
type envBinding struct {
	path Path
	name string
}

// failClosed is one FailClosed option.
type failClosed struct {
	path  Path
	order []json.RawMessage
}

// sectionSpec is what Define's options record. F3-01's Register validates it.
type sectionSpec struct {
	path        Path
	defaults    any
	permRead    string
	permWrite   string
	docs        map[Path]string
	waivers     map[Path]string
	envBindings []envBinding
	failClosed  []failClosed
}

// SectionOption configures a Section at Define time.
type SectionOption func(*sectionSpec)

// Permissions overrides the rbac strings a section's keys are read and written
// with; the default is derived from the path (05-config §1.3.4).
func Permissions(read, write string) SectionOption {
	return func(s *sectionSpec) { s.permRead, s.permWrite = read, write }
}

// Docs gives key -> description for the schema and the UI.
func Docs(d map[Path]string) SectionOption {
	d = maps.Clone(d)
	return func(s *sectionSpec) {
		if s.docs == nil {
			s.docs = make(map[Path]string, len(d))
		}
		maps.Copy(s.docs, d)
	}
}

// ProtectionWaivers gives key -> the reason it needs no Protection class
// despite its shape (05-config §1.3.7).
func ProtectionWaivers(w map[Path]string) SectionOption {
	w = maps.Clone(w)
	return func(s *sectionSpec) {
		if s.waivers == nil {
			s.waivers = make(map[Path]string, len(w))
		}
		maps.Copy(s.waivers, w)
	}
}

// EnvBinding gives key p one more env name, for an ecosystem name only
// (OTEL_EXPORTER_OTLP_ENDPOINT and its siblings, 05-metrics §10). The
// canonical name keeps working; the two in one layer follow 05-config
// §1.3.5's conflict rule. A name bound twice, or equal to any key's canonical
// name, fails Register (F3-01) with ErrDuplicate.
func EnvBinding(p Path, name string) SectionOption {
	return func(s *sectionSpec) { s.envBindings = append(s.envBindings, envBinding{p, name}) }
}

// FailClosed marks a bool or enum key whose more restrictive value wins between
// the operator layers and the DB layer (05-config §1.3.5). order lists every
// allowed value as JSON, most restrictive first. This package only records it:
// Register (F3-01) fails with ErrInvalid when the key is not a bool or enum,
// when a value is missing from order, or for a secret key.
func FailClosed(p Path, order ...json.RawMessage) SectionOption {
	// Cloned when the option is built, so a caller editing its slice or bytes
	// afterwards cannot reach the section; each spec() then gets its own copy.
	kept := make([]json.RawMessage, len(order))
	for i, o := range order {
		kept[i] = slices.Clone(o)
	}
	return func(s *sectionSpec) {
		cp := make([]json.RawMessage, len(kept))
		for i, o := range kept {
			cp[i] = slices.Clone(o)
		}
		s.failClosed = append(s.failClosed, failClosed{p, cp})
	}
}
