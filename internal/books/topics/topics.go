// file: internal/books/topics/topics.go
// version: 0.1.0
// guid: 970a1740-8f7b-46cc-a2fc-8b197697a28e
// last-edited: 2026-11-20

// Package topics is books' catalog of notifiable event topics for the
// integrations flip (F7-09): the twelve events HEAD's plugin bus carries
// (internal/plugin/events.go), each as a typed payload, a topic and a catalog
// entry that marks it notifiable (05-notify section 10, 05-events section 5E.3).
//
// It is dark. Nothing declares these entries until the flip registers them with
// Registrar.Topics and books publishes through Deps.Notify; until then HEAD's
// plugin bus and the webhook plugin stay in force and this package has no caller.
//
// The topics are bus topics with Notify metadata, not durable ones: a notification
// is made durable when Deps.Notify.Publish stages its delivery, in the batch of
// the change it announces (COR2-3), so the topic itself needs no outbox.
//
// Payloads are v1 contracts. Seven carry the fields HEAD's publishers put in the
// event's Data map, as typed fields. Five events (tags.written, dedup.detected,
// cover.changed, read_status.changed, scan.completed) have no publisher at HEAD, so
// there is no HEAD shape to carry: their payloads hold the least that identifies
// the subject, and a field added later is additive within v1.
package topics

import (
	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/rbac"
)

// Module is books' module ID, the namespace of every topic here.
const Module = "books"

// The topic names (05-notify section 10's map; the grammar of 03 section 3.6).
const (
	BookImportedName      = "books.book.imported.v1"
	BookDeletedName       = "books.book.deleted.v1"
	MetadataAppliedName   = "books.metadata.applied.v1"
	TagsWrittenName       = "books.tags.written.v1"
	FileOrganizedName     = "books.file.organized.v1"
	DedupDetectedName     = "books.dedup.detected.v1"
	DedupMergedName       = "books.dedup.merged.v1"
	CoverChangedName      = "books.cover.changed.v1"
	ReadStatusChangedName = "books.read-status.changed.v1"
	ScanCompletedName     = "books.scan.completed.v1"
	BookQuarantinedName   = "books.book.quarantined.v1"
	BookUnquarantinedName = "books.book.unquarantined.v1"
)

// view is the permission that observes every topic: whoever may view the library
// (the precedent is books.book.relocated.v1). A topic's permission is checked per
// subscriber on every surface, and a notification rule may name only topics its
// author may observe.
var view = rbac.Need("books:view")

// BookImportedV1 is the payload of books.book.imported.v1. Source is "import"
// for a file imported through the filesystem handler and "itunes" for a book the
// iTunes import created; FilePath is set for the first only, as HEAD sent it.
type BookImportedV1 struct {
	BookID   string `json:"book_id"`
	Source   string `json:"source"`
	FilePath string `json:"file_path,omitempty"`
}

// BookDeletedV1 is the payload of books.book.deleted.v1.
type BookDeletedV1 struct {
	BookID     string `json:"book_id"`
	SoftDelete bool   `json:"soft_delete"`
	BlockHash  bool   `json:"block_hash"`
}

// MetadataAppliedV1 is the payload of books.metadata.applied.v1: the source the
// metadata came from and the message its application reported.
type MetadataAppliedV1 struct {
	BookID  string `json:"book_id"`
	Source  string `json:"source"`
	Message string `json:"message,omitempty"`
}

// TagsWrittenV1 is the payload of books.tags.written.v1. HEAD has no publisher.
type TagsWrittenV1 struct {
	BookID string `json:"book_id"`
}

// FileOrganizedV1 is the payload of books.file.organized.v1. OriginalBookID is
// set when the organize created a new book from an existing one (HEAD's copy
// path), and BookID is then the new book.
type FileOrganizedV1 struct {
	BookID         string `json:"book_id"`
	OldPath        string `json:"old_path"`
	NewPath        string `json:"new_path"`
	OperationID    string `json:"operation_id,omitempty"`
	OriginalBookID string `json:"original_book_id,omitempty"`
}

// DedupDetectedV1 is the payload of books.dedup.detected.v1. HEAD has no
// publisher.
type DedupDetectedV1 struct {
	CandidateID string `json:"candidate_id"`
	EntityType  string `json:"entity_type"`
}

// DedupMergedV1 is the payload of books.dedup.merged.v1. EntityAID is the book
// HEAD keyed the event by. AlreadyMerged is true when the source book had been
// merged away before the merge ran and nothing changed.
type DedupMergedV1 struct {
	EntityAID     string `json:"entity_a_id"`
	EntityBID     string `json:"entity_b_id"`
	EntityType    string `json:"entity_type"`
	CandidateID   string `json:"candidate_id"`
	AlreadyMerged bool   `json:"already_merged,omitempty"`
}

// CoverChangedV1 is the payload of books.cover.changed.v1. HEAD has no publisher.
type CoverChangedV1 struct {
	BookID string `json:"book_id"`
}

// ReadStatusChangedV1 is the payload of books.read-status.changed.v1. HEAD has no
// publisher. The payload names no user: a notification is global (05-notify
// section 1), and a user's reading state is not for every rule's destination.
type ReadStatusChangedV1 struct {
	BookID string `json:"book_id"`
}

// ScanCompletedV1 is the payload of books.scan.completed.v1. HEAD has no
// publisher.
type ScanCompletedV1 struct {
	OperationID string `json:"operation_id"`
}

// BookQuarantinedV1 is the payload of books.book.quarantined.v1. QuarantinedAt is
// RFC 3339, as HEAD sent it.
type BookQuarantinedV1 struct {
	BookID        string `json:"book_id"`
	Title         string `json:"title"`
	Author        string `json:"author"`
	FilePath      string `json:"file_path"`
	OriginalPath  string `json:"original_path"`
	Reason        string `json:"reason"`
	QuarantinedAt string `json:"quarantined_at"`
}

// BookUnquarantinedV1 is the payload of books.book.unquarantined.v1.
type BookUnquarantinedV1 struct {
	BookID         string `json:"book_id"`
	FilePath       string `json:"file_path"`
	QuarantinePath string `json:"quarantine_path"`
}

// The typed topics. Functions, not package variables (R5: no mutable globals).

// BookImported is the typed topic of books.book.imported.v1.
func BookImported() events.Topic[BookImportedV1] { return events.NewTopic[BookImportedV1](BookImportedName) }

// BookDeleted is the typed topic of books.book.deleted.v1.
func BookDeleted() events.Topic[BookDeletedV1] { return events.NewTopic[BookDeletedV1](BookDeletedName) }

// MetadataApplied is the typed topic of books.metadata.applied.v1.
func MetadataApplied() events.Topic[MetadataAppliedV1] {
	return events.NewTopic[MetadataAppliedV1](MetadataAppliedName)
}

// TagsWritten is the typed topic of books.tags.written.v1.
func TagsWritten() events.Topic[TagsWrittenV1] { return events.NewTopic[TagsWrittenV1](TagsWrittenName) }

// FileOrganized is the typed topic of books.file.organized.v1.
func FileOrganized() events.Topic[FileOrganizedV1] {
	return events.NewTopic[FileOrganizedV1](FileOrganizedName)
}

// DedupDetected is the typed topic of books.dedup.detected.v1.
func DedupDetected() events.Topic[DedupDetectedV1] {
	return events.NewTopic[DedupDetectedV1](DedupDetectedName)
}

// DedupMerged is the typed topic of books.dedup.merged.v1.
func DedupMerged() events.Topic[DedupMergedV1] { return events.NewTopic[DedupMergedV1](DedupMergedName) }

// CoverChanged is the typed topic of books.cover.changed.v1.
func CoverChanged() events.Topic[CoverChangedV1] {
	return events.NewTopic[CoverChangedV1](CoverChangedName)
}

// ReadStatusChanged is the typed topic of books.read-status.changed.v1.
func ReadStatusChanged() events.Topic[ReadStatusChangedV1] {
	return events.NewTopic[ReadStatusChangedV1](ReadStatusChangedName)
}

// ScanCompleted is the typed topic of books.scan.completed.v1.
func ScanCompleted() events.Topic[ScanCompletedV1] {
	return events.NewTopic[ScanCompletedV1](ScanCompletedName)
}

// BookQuarantined is the typed topic of books.book.quarantined.v1.
func BookQuarantined() events.Topic[BookQuarantinedV1] {
	return events.NewTopic[BookQuarantinedV1](BookQuarantinedName)
}

// BookUnquarantined is the typed topic of books.book.unquarantined.v1.
func BookUnquarantined() events.Topic[BookUnquarantinedV1] {
	return events.NewTopic[BookUnquarantinedV1](BookUnquarantinedName)
}

// notifiable is the catalog metadata of a notifiable topic.
func notifiable(title, severity string) *events.NotifyMeta {
	return &events.NotifyMeta{Title: title, DefaultSeverity: severity}
}

// Specs is the twelve catalog entries, in the order of HEAD's event types
// (internal/plugin/events.go), for books' Registrar.Topics at the flip. A fresh
// slice per call. Each is a bus topic: view permission, Notify metadata, no
// outbox. Only a quarantine warns by default; the rest are routine.
func Specs() []events.TopicSpec {
	ts := func(s events.TopicSpec) events.TopicSpec { s.Permission = view; return s }
	return []events.TopicSpec{
		ts(BookImported().Spec(events.TopicSpec{Notify: notifiable("Book imported", "info")})),
		ts(BookDeleted().Spec(events.TopicSpec{Notify: notifiable("Book deleted", "info")})),
		ts(MetadataApplied().Spec(events.TopicSpec{Notify: notifiable("Metadata applied", "info")})),
		ts(TagsWritten().Spec(events.TopicSpec{Notify: notifiable("Tags written", "info")})),
		ts(FileOrganized().Spec(events.TopicSpec{Notify: notifiable("File organized", "info")})),
		ts(DedupDetected().Spec(events.TopicSpec{Notify: notifiable("Duplicate detected", "info")})),
		ts(DedupMerged().Spec(events.TopicSpec{Notify: notifiable("Duplicates merged", "info")})),
		ts(CoverChanged().Spec(events.TopicSpec{Notify: notifiable("Cover changed", "info")})),
		ts(ReadStatusChanged().Spec(events.TopicSpec{Notify: notifiable("Read status changed", "info")})),
		ts(ScanCompleted().Spec(events.TopicSpec{Notify: notifiable("Scan completed", "info")})),
		ts(BookQuarantined().Spec(events.TopicSpec{Notify: notifiable("Book quarantined", "warning")})),
		ts(BookUnquarantined().Spec(events.TopicSpec{Notify: notifiable("Book released from quarantine", "info")})),
	}
}

// HEADEvents maps HEAD's plugin event type (internal/plugin/events.go) to its
// topic name (05-notify section 10). A fresh map per call.
func HEADEvents() map[string]string {
	return map[string]string{
		"book.imported":       BookImportedName,
		"book.deleted":        BookDeletedName,
		"metadata.applied":    MetadataAppliedName,
		"tags.written":        TagsWrittenName,
		"file.organized":      FileOrganizedName,
		"dedup.detected":      DedupDetectedName,
		"dedup.merged":        DedupMergedName,
		"cover.changed":       CoverChangedName,
		"read_status.changed": ReadStatusChangedName,
		"scan.completed":      ScanCompletedName,
		"book.quarantined":    BookQuarantinedName,
		"book.unquarantined":  BookUnquarantinedName,
	}
}
