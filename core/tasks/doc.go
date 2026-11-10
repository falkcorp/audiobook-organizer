// file: core/tasks/doc.go
// version: 1.0.0
// guid: 5cd900ae-ddea-4a10-bb78-ad268c0f6904
// last-edited: 2026-11-10

// Package tasks is core's one durable background-work system (05-tasks): the
// definition types Task, Batch and Fixer, the declared Parallelism every loop
// needs, the pure loops RunItems, RunSeq and ForEach, and the interfaces the
// executor, the scheduler, the native stores and the HTTP routes implement.
//
// This package holds the public surface only. The executor, its run context,
// CPU budget, pause gate and ref-lock table live in core/internal/executor and
// reach RunItems through the run handle an *RC wraps; with a plain context,
// ForEach is a bounded worker pool and nothing more. There is no
// package-level mutable state (03 R5): everything stateful is a field of an
// executor instance, so two apps in one process share nothing.
//
// Apps build definitions with Task, Batch and Fixer and register them through
// module.Registrar. Constructors never panic: problems are collected on the
// definition, Problems reports them, and registration fails app.New on any
// (fail-closed, 05-tasks §5T.3.1).
package tasks
