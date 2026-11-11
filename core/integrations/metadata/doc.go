// file: core/integrations/metadata/doc.go
// version: 1.0.0
// guid: eec3b0c6-c798-4a11-ae07-5b6d0ce3b0d9
// last-edited: 2026-11-11

// Package metadata is the generic metadata-provider contract of 05-metadata
// M3: Provider[Q, R] (the query and result types are the app's), the
// hold-aware Chain with bounded fan-out, and the read-through answer Cache
// with positive and negative TTLs and single-flight. Ranking, merging and
// field-level application stay app-side.
//
// Provider adapters build their HTTP client from Env.HTTP (M2), so every
// request passes through providerhttp's gate: the chain does not re-implement
// holds, breakers or quotas, it reads their refusals (providerhttp.ReasonOf)
// and reports them as Skipped outcomes.
//
// Layer L4 (03 R4); it holds no package-level mutable state (R5).
package metadata
