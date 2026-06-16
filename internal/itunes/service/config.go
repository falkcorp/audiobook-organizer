// file: internal/itunes/service/config.go
// version: 1.2.0
// guid: 6d05155e-42e3-4319-a2a7-2e80d10be2aa

package itunesservice

import "time"

// Config is the iTunes-specific slice of config.AppConfig, passed by
// value at construction so the service has no transitive dependency on
// the global config singleton.
type Config struct {
	Enabled             bool
	LibraryReadPath     string
	LibraryWritePath    string
	DefaultMappings     []PathMapping
	SyncInterval        time.Duration
	WriteBackInterval   time.Duration
	WriteBackMaxBatch   int
	BackupKeep          int
	ImportConcurrency   int
	AutoWriteBack       bool // mirror of config.AppConfig.ITunes.AutoWriteBack
	ITLWriteBackEnabled bool // mirror of config.AppConfig.ITunes.WriteBackEnabled
}

// PathMapping is a single ITunesPath → OrganizedPath transform applied
// during import when iTunes PIDs resolve to a different filesystem
// location than the library's canonical layout.
type PathMapping struct {
	From string
	To   string
}
