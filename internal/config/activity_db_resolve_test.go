// file: internal/config/activity_db_resolve_test.go
// version: 1.0.0
// guid: b52d7f36-4c19-4e8a-90d3-6f7c2a15e084
// last-edited: 2026-09-07

package config

import (
	"path/filepath"
	"testing"
)

// TestResolveActivityDBPath covers the three ways the activity database's
// location is decided, in priority order.
func TestResolveActivityDBPath(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{
			name: "an explicit setting wins over every default",
			cfg: Config{
				ActivityDBPath: "/srv/custom/activity.sqlite",
				RootDir:        "/mnt/bigdata/books/audiobook-organizer",
				DatabasePath:   "/var/lib/audiobook-organizer/db",
			},
			want: "/srv/custom/activity.sqlite",
		},
		{
			name: "the default is a dot-directory under the library root",
			cfg: Config{
				RootDir:      "/mnt/bigdata/books/audiobook-organizer",
				DatabasePath: "/var/lib/audiobook-organizer/db",
			},
			want: "/mnt/bigdata/books/audiobook-organizer/.activity/activity.sqlite",
		},
		{
			name: "with no library root it falls back beside the main database",
			cfg: Config{
				DatabasePath: "/var/lib/audiobook-organizer/db/main.db",
			},
			want: "/var/lib/audiobook-organizer/db/activity.sqlite",
		},
		{
			name: "whitespace is not a configured path",
			cfg: Config{
				ActivityDBPath: "   ",
				RootDir:        "/library",
				DatabasePath:   "/var/db/main.db",
			},
			want: filepath.Join("/library", ".activity", "activity.sqlite"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.ResolveActivityDBPath(); got != tt.want {
				t.Errorf("ResolveActivityDBPath() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestResolveActivityDBPath_NeverResolvesToTheFilesystemRoot guards the specific
// way the RootDir default could go wrong. An empty RootDir joined blindly would
// yield "/.activity/activity.sqlite" — a path outside any configured directory,
// usually unwritable, and on a system that ran as root a file dropped at /.
func TestResolveActivityDBPath_NeverResolvesToTheFilesystemRoot(t *testing.T) {
	c := Config{DatabasePath: "/var/lib/audiobook-organizer/db/main.db"} // RootDir empty
	got := c.ResolveActivityDBPath()
	if got == "/"+ActivityDBDirName+"/"+ActivityDBFileName {
		t.Fatalf("resolved to the filesystem root: %q", got)
	}
	if want := "/var/lib/audiobook-organizer/db/activity.sqlite"; got != want {
		t.Errorf("got %q, want the fallback beside the database %q", got, want)
	}
}
