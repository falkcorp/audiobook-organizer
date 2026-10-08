// file: internal/itunes/service/config.go
// version: 1.4.0
// guid: 6d05155e-42e3-4319-a2a7-2e80d10be2aa
// last-edited: 2026-10-08

package itunesservice

// Config is the iTunes-specific slice of config.AppConfig, passed by
// value at construction so the service has no transitive dependency on
// the global config singleton.
type Config struct {
	Enabled           bool
	LibraryReadPath   string
	DefaultMappings   []PathMapping
	ImportConcurrency int
}

// PathMapping is a single ITunesPath → OrganizedPath transform applied
// during import when iTunes PIDs resolve to a different filesystem
// location than the library's canonical layout.
type PathMapping struct {
	From string
	To   string
}
