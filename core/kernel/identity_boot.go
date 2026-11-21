// file: core/kernel/identity_boot.go
// version: 0.1.0
// guid: 6c15ce56-019c-47fc-85c6-5e76877470a6
// last-edited: 2026-11-21

package kernel

// This file is the kernel's identity boot (F4-08 B3): the call sites that wire
// the dark identity building blocks (identity.go's buildIdentity, p4d_steps.go's
// runP4dSteps, the forwarding importers, the events bus) into the boot phases of
// 06 §6.5.1. Everything here is reached only when the app declares the identity
// flip (identityBootDeclared); an app that does not boots exactly as before.

// identityBootDeclared reports whether the app declared the identity flip
// (Config.IdentityBoot, from app.Options.IdentityBoot). It is one explicit
// flag read in one place, never derived from another field: app.Options.Auth's
// zero value is what hello passes and ABO sets Auth before its flip, so no
// field of the app's options says "the app wants core identity" except this
// one. A derived signal also failed once already (F3-07: a gate derived from
// other state broke under the crash-matrix injector). The boot phases ask this
// method and nothing else.
func (k *Kernel) identityBootDeclared() bool { return k.cfg.IdentityBoot }
