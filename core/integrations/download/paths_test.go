// file: core/integrations/download/paths_test.go
// version: 1.1.0
// guid: daba7df7-02a3-4d43-ae3b-cd72d7252769
// last-edited: 2026-11-11

package download_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/falkcorp/mediacore/integrations/download"
)

// mapperOf is a PathMapper over rows for the one source "download:box".
func mapperOf(rows ...download.Mapping) download.PathMapper {
	return download.NewMapper(func(source string) []download.Mapping {
		if source == download.Source("box") {
			return rows
		}
		return nil
	})
}

func TestPaths_WindowsRemote(t *testing.T) {
	win := mapperOf(download.Mapping{Remote: `D:\dl`, Local: "/mnt/dl"})
	fold := mapperOf(download.Mapping{Remote: `D:\dl`, Local: "/mnt/dl", Fold: true})
	for _, tc := range []struct {
		name   string
		m      download.PathMapper
		remote string
		want   download.LocalPath // "" = unmapped
	}{
		{"backslashes", win, `D:\dl\Hush\a.m4b`, "/mnt/dl/Hush/a.m4b"},
		{"forward slashes", win, "D:/dl/Hush/a.m4b", "/mnt/dl/Hush/a.m4b"},
		{"the root itself", win, `D:\dl`, "/mnt/dl"},
		{"the root with a trailing separator", win, `D:\dl` + "\\", "/mnt/dl"},
		{"drive letter case differs, no fold", win, `d:\dl\Hush`, ""},
		{"directory case differs, no fold", win, `D:\DL\Hush`, ""},
		{"case differs, fold", fold, `d:\DL\Hush\a.m4b`, "/mnt/dl/Hush/a.m4b"},
		{"sibling sharing a prefix", win, `D:\dl-old\Hush`, ""},
		{"sibling sharing a prefix, fold", fold, `D:\DL-old\Hush`, ""},
		{"another drive", win, `E:\dl\Hush`, ""},
		{"file URL", win, "file:///D:/dl/My%20Book/a.m4b", "/mnt/dl/My Book/a.m4b"},
		{"file URL, localhost", win, "file://localhost/D:/dl/Hush", "/mnt/dl/Hush"},
		{"file URL with a host", win, "file://seedbox/D:/dl/Hush", ""},
		{"file URL hiding a separator", win, "file:///D:/dl/a%2F..%2Fb", ""},
		{"UNC", win, `\\seedbox\share\Hush`, ""},
		{"traversal", win, `D:\dl\..\secret`, ""},
		{"traversal in the middle", win, `D:\dl\Hush\..\..\secret`, ""},
		{"relative", win, `dl\Hush`, ""},
		{"empty", win, "", ""},
		{"a POSIX path is not a drive path", win, "/dl/Hush", ""},
		{"repeated and dot segments", win, `D:\dl\Hush\.\a.m4b`, "/mnt/dl/Hush/a.m4b"},
	} {
		got, err := tc.m.ToLocal(download.Source("box"), tc.remote)
		if tc.want == "" {
			if !errors.Is(err, download.ErrUnmapped) || got != "" {
				t.Errorf("%s: ToLocal(%q) = %q, %v; want ErrUnmapped", tc.name, tc.remote, got, err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%s: ToLocal(%q) = %q, %v; want %q", tc.name, tc.remote, got, err, tc.want)
		}
	}
}

func TestPaths_PosixRemote(t *testing.T) {
	m := mapperOf(
		download.Mapping{Remote: "/downloads", Local: "/srv/dl"},
		download.Mapping{Remote: "/downloads/audiobooks", Local: "/srv/library/incoming"},
		download.Mapping{Remote: "/downloads/audiobooks/archive", Local: "/srv/archive", Fold: true},
		download.Mapping{Remote: "relative/ignored", Local: "/srv/never"},
		download.Mapping{Remote: "/bad-local", Local: "relative"},
	)
	for _, tc := range []struct {
		remote string
		want   download.LocalPath
	}{
		{"/downloads/comics/x", "/srv/dl/comics/x"},
		{"/downloads/audiobooks/Hush/a.m4b", "/srv/library/incoming/Hush/a.m4b"}, // the longest prefix wins
		{"/downloads/audiobooks", "/srv/library/incoming"},
		{"/downloads/audiobooks-extra/x", "/srv/dl/audiobooks-extra/x"}, // a segment, not a string prefix
		{"/downloads/Audiobooks/x", "/srv/dl/Audiobooks/x"},             // POSIX is case-sensitive
		{"/downloads/audiobooks/ARCHIVE/x", "/srv/archive/x"},           // Fold is per mapping
		{"/downloads/audiobooks/a\\b", "/srv/library/incoming/a\\b"},    // a backslash is a name character
		{"/downloads/../etc/passwd", ""},
		{"/other/x", ""},
		{"/bad-local/x", ""},
		{"downloads/x", ""},
	} {
		got, err := m.ToLocal(download.Source("box"), tc.remote)
		if tc.want == "" {
			if !errors.Is(err, download.ErrUnmapped) {
				t.Errorf("ToLocal(%q) = %q, %v; want ErrUnmapped", tc.remote, got, err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("ToLocal(%q) = %q, %v; want %q", tc.remote, got, err, tc.want)
		}
	}
	if _, err := m.ToLocal("download:other", "/downloads/x"); !errors.Is(err, download.ErrUnmapped) {
		t.Errorf("another source's mappings applied: %v", err)
	}
}

func TestPaths_ToRemote(t *testing.T) {
	win := mapperOf(download.Mapping{Remote: `D:\dl`, Local: "/mnt/dl"})
	posix := mapperOf(
		download.Mapping{Remote: "/remote/library", Local: "/srv/library"},
		download.Mapping{Remote: "/remote/library/rare", Local: "/srv/library/rare"},
	)
	for _, tc := range []struct {
		name  string
		m     download.PathMapper
		local download.LocalPath
		want  string // "" = unmapped
	}{
		{"windows style", win, "/mnt/dl/Hush", `D:\dl\Hush`},
		{"windows root", win, "/mnt/dl", `D:\dl`},
		{"posix", posix, "/srv/library/Batman/Hush", "/remote/library/Batman/Hush"},
		{"longest local prefix", posix, "/srv/library/rare/x", "/remote/library/rare/x"},
		{"a local sibling sharing a prefix", posix, "/srv/library-old/x", ""},
		{"a host path outside every mapping", posix, "/tmp/x", ""},
		{"not absolute", posix, "srv/library/x", ""},
		{"cleaned before matching", posix, "/srv/library/a/../b", "/remote/library/b"},
		{"traversal out of the mapping", posix, "/srv/library/../etc", ""},
	} {
		got, err := tc.m.ToRemote(download.Source("box"), tc.local)
		if tc.want == "" {
			if !errors.Is(err, download.ErrUnmapped) || got != "" {
				t.Errorf("%s: ToRemote(%q) = %q, %v; want ErrUnmapped", tc.name, tc.local, got, err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%s: ToRemote(%q) = %q, %v; want %q", tc.name, tc.local, got, err, tc.want)
		}
	}
}

// What the client reports can be mapped to the host and back to the client.
func TestPaths_RoundTrip(t *testing.T) {
	for _, row := range []download.Mapping{
		{Remote: `D:\dl`, Local: "/mnt/dl"},
		{Remote: "/remote/books", Local: "/srv/library/incoming"},
	} {
		m := mapperOf(row)
		remote := row.Remote + "/Batman/Hush"
		if row.Remote[0] != '/' {
			remote = row.Remote + `\Batman\Hush`
		}
		local, err := m.ToLocal(download.Source("box"), remote)
		if err != nil {
			t.Fatalf("%s: %v", row.Remote, err)
		}
		back, err := m.ToRemote(download.Source("box"), local)
		if err != nil || back != remote {
			t.Errorf("%s: %q -> %q -> %q, %v", row.Remote, remote, local, back, err)
		}
	}
}

// A host path segment is data in the client's syntax: ToRemote must never let
// one add a separator, a "..", a drive switch or a NUL to the client's path,
// and the result must stay under the mapping's remote root.
func TestPaths_ToRemoteRefusesInjectedSyntax(t *testing.T) {
	win := mapperOf(download.Mapping{Remote: `D:\dl`, Local: "/mnt/dl"})
	winURL := mapperOf(download.Mapping{Remote: "file:///D:/dl", Local: "/mnt/dl"})
	posix := mapperOf(download.Mapping{Remote: "/remote/library", Local: "/srv/library"})
	for _, tc := range []struct {
		name  string
		m     download.PathMapper
		local download.LocalPath
	}{
		{"windows: backslash traversal in one segment", win, `/mnt/dl/a\..\..\secret`},
		{"windows: a trailing backslash dot-dot", win, `/mnt/dl/Hush\..`},
		{"windows: backslash separator", win, `/mnt/dl/Hush\a.m4b`},
		{"windows: drive switch", win, "/mnt/dl/E:"},
		{"windows: colon in a name", win, "/mnt/dl/Hush:Year One"},
		{"windows: NUL", win, "/mnt/dl/Hush\x00a"},
		{"windows via a file URL root: backslash traversal", winURL, `/mnt/dl/a\..\..\secret`},
		{"windows via a file URL root: drive switch", winURL, "/mnt/dl/C:"},
		{"posix: NUL", posix, "/srv/library/Hush\x00a"},
		{"posix: traversal out of the root", posix, "/srv/library/../etc/passwd"},
		{"windows: traversal out of the root", win, "/mnt/dl/../secret"},
	} {
		got, err := tc.m.ToRemote(download.Source("box"), tc.local)
		if !errors.Is(err, download.ErrUnmapped) || got != "" {
			t.Errorf("%s: ToRemote(%q) = %q, %v; want ErrUnmapped", tc.name, tc.local, got, err)
		}
	}

	// What stays legal: a POSIX client takes backslash and colon in a name; a
	// Windows client takes dots and spaces inside a name.
	for _, tc := range []struct {
		m     download.PathMapper
		local download.LocalPath
		want  string
	}{
		{posix, `/srv/library/Hush\Year:One`, `/remote/library/Hush\Year:One`},
		{win, "/mnt/dl/Hush. Year One/a..b.m4b", `D:\dl\Hush. Year One\a..b.m4b`},
	} {
		got, err := tc.m.ToRemote(download.Source("box"), tc.local)
		if err != nil || got != tc.want {
			t.Errorf("ToRemote(%q) = %q, %v; want %q", tc.local, got, err, tc.want)
		}
	}
}

// ToLocal never returns a path that leaves its mapping's Local root, whatever
// the client reports.
func TestPaths_ToLocalRefusesInjectedSyntax(t *testing.T) {
	posix := mapperOf(download.Mapping{Remote: "/remote/library", Local: "/srv/library"})
	win := mapperOf(download.Mapping{Remote: `D:\dl`, Local: "/mnt/dl"})
	for _, tc := range []struct {
		name   string
		m      download.PathMapper
		remote string
	}{
		{"posix: dot-dot", posix, "/remote/library/../etc/passwd"},
		{"posix: encoded separator in a file URL", mapperOf(download.Mapping{Remote: "file:///remote/library", Local: "/srv/library"}), "file:///remote/library/a%2F..%2F..%2Fx"},
		{"posix: NUL", posix, "/remote/library/a\x00b"},
		{"windows: dot-dot", win, `D:\dl\..\..\etc`},
		{"windows: encoded backslash in a file URL", mapperOf(download.Mapping{Remote: "file:///D:/dl", Local: "/mnt/dl"}), "file:///D:/dl/a%5C..%5Cx"},
		{"windows: NUL", win, "D:\\dl\\a\x00b"},
	} {
		got, err := tc.m.ToLocal(download.Source("box"), tc.remote)
		if !errors.Is(err, download.ErrUnmapped) || got != "" {
			t.Errorf("%s: ToLocal(%q) = %q, %v; want ErrUnmapped", tc.name, tc.remote, got, err)
		}
	}

	// Names that are only data on a POSIX host stay mapped there; on a host
	// whose separator is a backslash they are separators and are refused.
	got, err := posix.ToLocal(download.Source("box"), `/remote/library/Hush\Year:One`)
	if filepath.Separator == '/' {
		if err != nil || got != `/srv/library/Hush\Year:One` {
			t.Errorf("ToLocal of a backslash name = %q, %v", got, err)
		}
	} else if !errors.Is(err, download.ErrUnmapped) {
		t.Errorf("ToLocal of a backslash name on this host = %q, %v; want ErrUnmapped", got, err)
	}
}
