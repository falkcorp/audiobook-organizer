// file: internal/deluge/discovery_fingerprint_test.go
// version: 1.0.0
// guid: 1af9e6b0-8cc6-4be2-a92c-885e26107d35
// last-edited: 2026-09-19

package deluge

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
)

type countingFPStore struct{ exactQueries []string }

func (s *countingFPStore) GetBookByFileHash(string) (*database.Book, error) { return nil, nil }
func (s *countingFPStore) GetBookFileByAcoustID(fp string) (*database.BookFile, error) {
	s.exactQueries = append(s.exactQueries, fp)
	return &database.BookFile{ID: "hit"}, nil
}
func (s *countingFPStore) GetBookFileByAcoustIDFuzzy(string, float64) (*database.BookFile, error) {
	return nil, nil
}

// TestIsContentFingerprintTracked_DecodesOnlyTheHead pins the work the
// discovery check does per torrent: it uses only the head print, so it must
// run fpcalc once on the file and never start ffmpeg (no window/segment
// decodes, no ffprobe). Before 2026-09-19 it called FileSegments and cut all
// seven 300 s segments, discarding six.
func TestIsContentFingerprintTracked_DecodesOnlyTheHead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake binaries are /bin/sh scripts")
	}
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls.log")
	write := func(name, body string) string {
		p := filepath.Join(bin, name)
		script := "#!/bin/sh\necho " + name + " >> " + calls + "\n" + body + "\n"
		if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	fpcalc := write("fpcalc", `for a in "$@"; do last="$a"; done
if [ "$last" = "-" ]; then cat >/dev/null; printf '{"duration":0,"fingerprint":[1,2,3]}\n'
else printf '{"duration":3600.00,"fingerprint":"AQAAHEAD"}\n'; fi`)
	write("ffmpeg", "head -c 1000 /dev/zero")
	write("ffprobe", "echo 3600")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	fingerprint.SetResolvedFpcalcPath(fpcalc)
	t.Cleanup(func() { fingerprint.SetResolvedFpcalcPath("") })

	content := t.TempDir()
	if err := os.WriteFile(filepath.Join(content, "01.mp3"), []byte("not audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := &countingFPStore{}
	if !IsContentFingerprintTracked(store, content) {
		t.Fatal("head print matched in the store but the torrent was not reported tracked")
	}
	if len(store.exactQueries) != 1 || store.exactQueries[0] != "AQAAHEAD" {
		t.Errorf("exact lookups %v, want the head print once", store.exactQueries)
	}
	log, _ := os.ReadFile(calls)
	got := strings.Fields(string(log))
	if len(got) != 1 || got[0] != "fpcalc" {
		t.Errorf("tool invocations %v, want exactly [fpcalc]", got)
	}
}
