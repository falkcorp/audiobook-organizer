// file: pkg/plugin/sdk/operation.go
// version: 1.1.0
// guid: b2c3d4e5-f6a7-8901-bcde-f12345678901
// last-edited: 2026-08-07

package sdk

import "github.com/falkcorp/audiobook-organizer/internal/operations/registry"

// Type aliases for operation definition and control types.
type OperationDef = registry.OperationDef
type ResumePolicy = registry.ResumePolicy
type Priority = registry.Priority
type ActorMode = registry.ActorMode
type Phase = registry.Phase
type Resource = registry.Resource

// Resume policy constants.
const (
	ResumeUnspecified = registry.ResumeUnspecified
	ResumeRestart     = registry.ResumeRestart
	ResumeRequeue     = registry.ResumeRequeue
	ResumeDrop        = registry.ResumeDrop
	ResumeAsk         = registry.ResumeAsk
)

// Priority level constants.
const (
	PriorityLow    = registry.PriorityLow
	PriorityNormal = registry.PriorityNormal
	PriorityHigh   = registry.PriorityHigh
)

// Actor mode constants.
const (
	ActorContext = registry.ActorContext
	ActorSystem  = registry.ActorSystem
)

// Write-set resource constants for OperationDef.Writes / Reads.
// Declaring Writes lets the dispatcher's conflict gate keep two ops that
// mutate the same table from running concurrently (whole-row write-backs
// silently lose fields when interleaved). Empty Writes = undeclared = the
// gate ignores the op.
const (
	ResBooks       = registry.ResBooks
	ResBookFiles   = registry.ResBookFiles
	ResAuthors     = registry.ResAuthors
	ResSeries      = registry.ResSeries
	ResReviewItems = registry.ResReviewItems
	ResEmbeddings  = registry.ResEmbeddings
	ResOperations  = registry.ResOperations
)
