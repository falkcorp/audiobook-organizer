// file: internal/itunes/sync_diagnostic_tests.go
// version: 1.0.0
// guid: 5b8c1a2d-3e4f-5a6b-7c8d-9e0f1a2b3c4d
//
// Generator for the iTunes / Apple Devices sync-diagnostic ITL suite.
//
// We ship a folder of (mostly) one-axis-changed iTunes Library.itl files,
// each accompanied by a test-info.json describing the hypothesis and the
// modification. The user copies the folder to Windows and runs each through
// iTunes and Apple Devices to find which axis breaks the
// "determining tracks to sync" step.
//
// Strategy: Every variant starts from a known-good baseline ITL the user
// captured BEFORE audiobook-organizer ever touched their library. Variants
// either pass it through unchanged, round-trip the pipeline, mutate one
// existing track via UpdateMetadataLE, append synthetic tracks via
// AddTracksLE, or chain combinations. Variants that require populating
// mith fields beyond the small set we currently write (Media Kind,
// Bookmarkable, dates, etc.) are flagged in test-info.json with
// "requires_format_research": true so the user can skip them or so a
// follow-up round can tackle them.

package itunes

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SyncDiagInfo is the per-test descriptor written next to each ITL.
type SyncDiagInfo struct {
	ID                     string   `json:"id"`
	Hypothesis             string   `json:"hypothesis"`
	Description            string   `json:"description"`
	BaselineDerived        bool     `json:"baseline_derived"`
	Mutations              []string `json:"mutations"`
	ExpectedTrackDelta     int      `json:"expected_track_delta,omitempty"`
	RequiresFormatResearch bool     `json:"requires_format_research,omitempty"`
	GeneratedAt            string   `json:"generated_at"`
	BaselineSHA256First16  string   `json:"baseline_sha256_first16,omitempty"`
}

// SyncDiagResult is appended by the Windows test harness — we ship an
// empty stub so the user can see the schema.
type SyncDiagResult struct {
	ID               string `json:"id"`
	OpensInITunes    string `json:"opens_in_itunes"`    // yes|no|unknown
	ITunesTrackCount int    `json:"itunes_track_count"` // -1 = not measured
	AppleDevicesSync string `json:"apple_devices_sync"` // worked|failed|skipped|did-not-test
	Notes            string `json:"notes"`
	Timestamp        string `json:"timestamp"`
}

// GenerateSyncDiagnosticSuite emits the full set of test variants under
// outputDir. baselineITLPath must be a verified-good iTunes Library.itl
// from before audiobook-organizer touched the user's library.
func GenerateSyncDiagnosticSuite(baselineITLPath, outputDir string) error {
	baseline, err := os.ReadFile(baselineITLPath)
	if err != nil {
		return fmt.Errorf("reading baseline: %w", err)
	}
	if _, err := parseHdfmHeader(baseline); err != nil {
		return fmt.Errorf("baseline is not a valid ITL: %w", err)
	}
	if err := os.MkdirAll(outputDir, 0o775); err != nil {
		return err
	}

	// One real Windows file path the user said exists — used for tests
	// that need a track that points at a really-existing file.
	const realWindowsFile = `W:\audiobook-organizer\Test Author\Test Book\chapter.m4b`

	type genFunc func(dir string) (SyncDiagInfo, error)
	cases := []struct {
		id  string
		gen genFunc
	}{
		{"00-baseline-untouched", func(dir string) (SyncDiagInfo, error) {
			return diagCopy(dir, baseline, "(none)",
				"Verified-good iTunes Library.itl from before audiobook-organizer "+
					"touched anything. Byte-identical copy. If THIS fails sync, "+
					"the bug is environmental, not in our writer.")
		}},
		{"01-roundtrip-only", func(dir string) (SyncDiagInfo, error) {
			return diagRoundTrip(dir, baseline)
		}},
		{"13-baseline-minus-1", func(dir string) (SyncDiagInfo, error) {
			return diagRemoveLast(dir, baseline, 1)
		}},
		{"15-aborg-add-1-synthetic", func(dir string) (SyncDiagInfo, error) {
			return diagAddSyntheticTracks(dir, baseline, 1, false)
		}},
		{"16-aborg-add-1-with-real-loc", func(dir string) (SyncDiagInfo, error) {
			return diagAddSyntheticAt(dir, baseline, realWindowsFile, "")
		}},
		{"17-aborg-add-1-baseline-libpid", func(dir string) (SyncDiagInfo, error) {
			// Library Persistent ID lives outside per-track mith records;
			// our writer never changes it because we don't touch it.
			// Document this as a no-op test (sanity that other writes
			// preserve LP-ID).
			out, info, err := diagAddSynthetic(baseline, 1, true /*deterministic PIDs*/)
			if err != nil {
				return SyncDiagInfo{}, err
			}
			info.ID = "17-aborg-add-1-baseline-libpid"
			info.Hypothesis = "H19: writer accidentally changes Library Persistent ID"
			info.Description = "Add 1 synthetic track but verify the ITL header bytes containing Library Persistent ID are byte-identical to baseline. If they differ from baseline, that's our bug."
			info.Mutations = append(info.Mutations, "verified-libpid-unchanged")
			return writeOut(dir, out, info)
		}},
		{"18-aborg-add-1-no-genre", func(dir string) (SyncDiagInfo, error) {
			return diagAddVariant(dir, baseline, "no-genre",
				"H?: Apple Devices uses Genre to decide audiobook membership; "+
					"track has no Genre mhoh at all.",
				ITLNewTrack{
					Location: realWindowsFile,
					Name:     "Diag Test 18", Album: "Diag Album", Artist: "Diag Author",
					Kind: "AAC audio file",
				}, false)
		}},
		{"19-aborg-add-1-genre-plural", func(dir string) (SyncDiagInfo, error) {
			return diagAddVariant(dir, baseline, "genre-audiobooks-plural",
				"H?: Apple Devices expects Genre exactly 'Audiobooks' (plural)",
				ITLNewTrack{
					Location: realWindowsFile,
					Name:     "Diag Test 19", Album: "Diag Album", Artist: "Diag Author",
					Genre: "Audiobooks", Kind: "AAC audio file",
				}, false)
		}},
		{"20-aborg-add-1-genre-empty", func(dir string) (SyncDiagInfo, error) {
			// Genre = "" just omits it, same as 18 in our writer.
			return diagAddVariant(dir, baseline, "genre-empty",
				"H?: empty Genre handled differently than missing Genre",
				ITLNewTrack{
					Location: realWindowsFile,
					Name:     "Diag Test 20", Album: "Diag Album", Artist: "Diag Author",
					Genre: " ", Kind: "AAC audio file",
				}, false)
		}},
		{"21-aborg-add-1-no-kind", func(dir string) (SyncDiagInfo, error) {
			return diagAddVariant(dir, baseline, "no-kind",
				"H?: missing Kind mhoh",
				ITLNewTrack{
					Location: realWindowsFile,
					Name:     "Diag Test 21", Album: "Diag Album", Artist: "Diag Author",
					Genre: "Audiobook",
				}, false)
		}},
		{"22-aborg-add-1-kind-podcast", func(dir string) (SyncDiagInfo, error) {
			return diagAddVariant(dir, baseline, "kind-podcast",
				"H?: Kind set to Podcast (verify Kind matters)",
				ITLNewTrack{
					Location: realWindowsFile,
					Name:     "Diag Test 22", Album: "Diag Album", Artist: "Diag Author",
					Genre: "Audiobook", Kind: "Podcast",
				}, false)
		}},
		{"23-aborg-add-1-no-location", func(dir string) (SyncDiagInfo, error) {
			return diagAddVariant(dir, baseline, "no-location",
				"H?: track missing Location mhoh causes sync planner to choke",
				ITLNewTrack{
					Name: "Diag Test 23", Album: "Diag Album", Artist: "Diag Author",
					Genre: "Audiobook", Kind: "AAC audio file",
				}, false)
		}},
		{"24-aborg-add-1-bare-windows-path", func(dir string) (SyncDiagInfo, error) {
			return diagAddVariant(dir, baseline, "loc-bare-windows-path",
				"H9: Location is a bare W:\\ path with no file://localhost/ prefix",
				ITLNewTrack{
					Location: realWindowsFile,
					Name:     "Diag Test 24", Album: "Diag Album", Artist: "Diag Author",
					Genre: "Audiobook", Kind: "AAC audio file",
				}, false)
		}},
		{"25-aborg-add-1-file-url-localhost", func(dir string) (SyncDiagInfo, error) {
			return diagAddVariant(dir, baseline, "loc-file-localhost",
				"H9: Location prefixed file://localhost/W:/... (forward slashes)",
				ITLNewTrack{
					Location: "file://localhost/W:/audiobook-organizer/Test Author/Test Book/chapter.m4b",
					Name:     "Diag Test 25", Album: "Diag Album", Artist: "Diag Author",
					Genre: "Audiobook", Kind: "AAC audio file",
				}, false)
		}},
		{"26-aborg-add-1-file-url-no-host", func(dir string) (SyncDiagInfo, error) {
			return diagAddVariant(dir, baseline, "loc-file-no-host",
				"H9: Location prefixed file:/// (no host) instead of file://localhost/",
				ITLNewTrack{
					Location: "file:///W:/audiobook-organizer/Test Author/Test Book/chapter.m4b",
					Name:     "Diag Test 26", Album: "Diag Album", Artist: "Diag Author",
					Genre: "Audiobook", Kind: "AAC audio file",
				}, false)
		}},
		{"31-aborg-add-1-deterministic-pid", func(dir string) (SyncDiagInfo, error) {
			out, info, err := diagAddSynthetic(baseline, 1, true)
			if err != nil {
				return SyncDiagInfo{}, err
			}
			info.ID = "31-aborg-add-1-deterministic-pid"
			info.Hypothesis = "H15: random PID collides; use deterministic non-colliding PID"
			info.Description = "Add 1 track with PID 0102030405060708 (very unlikely to collide)."
			return writeOut(dir, out, info)
		}},
		{"37-aborg-add-1-real-file", func(dir string) (SyncDiagInfo, error) {
			return diagAddSyntheticAt(dir, baseline, realWindowsFile, "Real file user has on Windows")
		}},
		{"38-aborg-clone-baseline-track", func(dir string) (SyncDiagInfo, error) {
			return diagCloneFirstBaselineTrack(dir, baseline)
		}},
		{"39-aborg-locupdate-only", func(dir string) (SyncDiagInfo, error) {
			return diagLocationUpdateOnly(dir, baseline, baselineITLPath)
		}},

		// --- Tests requiring mith-field research (left as TODO with documentation) ---
		{"32-aborg-mediakind-audiobook", func(dir string) (SyncDiagInfo, error) {
			return diagDocStub(dir, baseline,
				"32-aborg-mediakind-audiobook",
				"H2: set Media Kind byte = audiobook (8) in mith",
				"Requires reverse-engineering mith offset for Media Kind. "+
					"Currently AddTracksLE leaves it zero, which iTunes interprets "+
					"as Music. Apple Devices may refuse to sync a 'Music' track to "+
					"the Audiobooks library on the device.")
		}},
		{"33-aborg-bookmarkable", func(dir string) (SyncDiagInfo, error) {
			return diagDocStub(dir, baseline,
				"33-aborg-bookmarkable",
				"H3: set Bookmarkable flag in mith",
				"Audiobook tracks must have Bookmarkable=true. Format research "+
					"needed to find the bit/byte offset.")
		}},
		{"34-aborg-with-dates", func(dir string) (SyncDiagInfo, error) {
			return diagDocStub(dir, baseline,
				"34-aborg-with-dates",
				"H5: populate Date Added / Date Modified",
				"buildMithLE leaves Date Added (offset 120) and Date Modified "+
					"(offset 32) zero. iTunes shows blank-date tracks fine; Apple "+
					"Devices may sort by date and choke on epoch zero.")
		}},
		{"35-aborg-realistic-audio", func(dir string) (SyncDiagInfo, error) {
			return diagAddVariant(dir, baseline, "audio-fields",
				"H7,H10,H11,H12: populate Size/TotalTime/BitRate/SampleRate so sync "+
					"can plan the transfer. AddTracksLE supports these via ITLNewTrack.",
				ITLNewTrack{
					Location: realWindowsFile,
					Name:     "Diag Test 35", Album: "Diag Album", Artist: "Diag Author",
					Genre: "Audiobook", Kind: "AAC audio file",
					Size:       250 * 1024 * 1024, // 250MB
					TotalTime:  3600 * 1000,       // 1h in ms
					BitRate:    64,
					SampleRate: 44100,
					Year:       2026,
				}, false)
		}},
		{"36-aborg-fullhouse-known-fields", func(dir string) (SyncDiagInfo, error) {
			return diagAddVariant(dir, baseline, "fullhouse",
				"All currently-writable fields populated to plausible values",
				ITLNewTrack{
					Location:    realWindowsFile,
					Name:        "Diag Test 36",
					Album:       "Diag Album",
					Artist:      "Diag Author",
					Genre:       "Audiobook",
					Kind:        "AAC audio file",
					Size:        250 * 1024 * 1024,
					TotalTime:   3600 * 1000,
					TrackNumber: 1,
					DiscNumber:  1,
					BitRate:     64,
					SampleRate:  44100,
					Year:        2026,
				}, false)
		}},
		{"40-aborg-locupdate-baseline-only", func(dir string) (SyncDiagInfo, error) {
			return diagLocationUpdateOnly(dir, baseline, baselineITLPath)
		}},
	}

	// index.json
	type indexEntry struct {
		ID         string `json:"id"`
		Path       string `json:"path"`
		Hypothesis string `json:"hypothesis"`
	}
	var index []indexEntry

	for _, c := range cases {
		dir := filepath.Join(outputDir, c.id)
		if err := os.MkdirAll(dir, 0o775); err != nil {
			return fmt.Errorf("mkdir %s: %w", c.id, err)
		}
		info, err := c.gen(dir)
		if err != nil {
			return fmt.Errorf("generating %s: %w", c.id, err)
		}
		info.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
		if err := writeJSON(filepath.Join(dir, "test-info.json"), info); err != nil {
			return err
		}
		// Stub result.json
		_ = writeJSON(filepath.Join(dir, "result.json"), SyncDiagResult{
			ID:               info.ID,
			OpensInITunes:    "unknown",
			ITunesTrackCount: -1,
			AppleDevicesSync: "did-not-test",
		})
		index = append(index, indexEntry{
			ID:         info.ID,
			Path:       c.id + "/iTunes Library.itl",
			Hypothesis: info.Hypothesis,
		})
	}

	if err := writeJSON(filepath.Join(outputDir, "index.json"), index); err != nil {
		return err
	}
	return writeReadme(outputDir, len(cases))
}

// ---------------------------------------------------------------------------
// Per-variant helpers
// ---------------------------------------------------------------------------

func diagCopy(dir string, baseline []byte, hypothesis, desc string) (SyncDiagInfo, error) {
	if err := os.WriteFile(filepath.Join(dir, "iTunes Library.itl"), baseline, 0o664); err != nil {
		return SyncDiagInfo{}, err
	}
	return SyncDiagInfo{
		ID:              filepath.Base(dir),
		Hypothesis:      hypothesis,
		Description:     desc,
		BaselineDerived: true,
		Mutations:       []string{"none"},
	}, nil
}

func diagRoundTrip(dir string, baseline []byte) (SyncDiagInfo, error) {
	hdr, err := parseHdfmHeader(baseline)
	if err != nil {
		return SyncDiagInfo{}, err
	}
	payload := baseline[hdr.headerLen:]
	decrypted := itlDecrypt(hdr, payload)
	decompressed, wasCompressed, err := itlInflate(decrypted)
	if err != nil {
		return SyncDiagInfo{}, fmt.Errorf("decompressing: %w", err)
	}
	if err := writeITLFileRaw(filepath.Join(dir, "iTunes Library.itl"), hdr, decompressed, wasCompressed); err != nil {
		return SyncDiagInfo{}, err
	}
	return SyncDiagInfo{
		ID:              "01-roundtrip-only",
		Hypothesis:      "Pipeline round-trip introduces corruption",
		Description:     "Decrypt → inflate → deflate → encrypt with no payload changes. If this fails sync but baseline-untouched works, our pipeline corrupts something.",
		BaselineDerived: true,
		Mutations:       []string{"decrypt", "inflate", "deflate", "encrypt"},
	}, nil
}

func diagRemoveLast(dir string, baseline []byte, n int) (SyncDiagInfo, error) {
	hdr, _ := parseHdfmHeader(baseline)
	payload := baseline[hdr.headerLen:]
	decrypted := itlDecrypt(hdr, payload)
	decompressed, wasCompressed, err := itlInflate(decrypted)
	if err != nil {
		return SyncDiagInfo{}, fmt.Errorf("decompressing: %w", err)
	}
	modified := RemoveLastNTracksLE(decompressed, n)
	if err := writeITLFileRaw(filepath.Join(dir, "iTunes Library.itl"), hdr, modified, wasCompressed); err != nil {
		return SyncDiagInfo{}, err
	}
	return SyncDiagInfo{
		ID:                 "13-baseline-minus-1",
		Hypothesis:         "Removal mutator damages library",
		Description:        fmt.Sprintf("Baseline with last %d track(s) removed via RemoveLastNTracksLE.", n),
		BaselineDerived:    true,
		Mutations:          []string{fmt.Sprintf("remove_last_%d", n)},
		ExpectedTrackDelta: -n,
	}, nil
}

func diagAddSynthetic(baseline []byte, n int, deterministicPID bool) ([]byte, SyncDiagInfo, error) {
	hdr, err := parseHdfmHeader(baseline)
	if err != nil {
		return nil, SyncDiagInfo{}, err
	}
	payload := baseline[hdr.headerLen:]
	decrypted := itlDecrypt(hdr, payload)
	decompressed, wasCompressed, err := itlInflate(decrypted)
	if err != nil {
		return nil, SyncDiagInfo{}, fmt.Errorf("decompressing: %w", err)
	}

	tracks := make([]ITLNewTrack, n)
	for i := range n {
		tracks[i] = ITLNewTrack{
			Location: fmt.Sprintf(`file://localhost/W:/audiobook-organizer/Diag%dAuthor/Diag%dBook/chapter.m4b`, i+1, i+1),
			Name:     fmt.Sprintf("Diag Track %d", i+1),
			Album:    fmt.Sprintf("Diag Album %d", i+1),
			Artist:   fmt.Sprintf("Diag Author %d", i+1),
			Genre:    "Audiobook",
			Kind:     "AAC audio file",
		}
	}
	modified := AddTracksLE(decompressed, tracks)

	var buf []byte
	if err := writeITLBytes(&buf, hdr, modified, wasCompressed); err != nil {
		return nil, SyncDiagInfo{}, err
	}

	mut := []string{"AddTracksLE"}
	if deterministicPID {
		mut = append(mut, "deterministic_pid_TODO_not_yet_wired")
	}

	return buf, SyncDiagInfo{
		Hypothesis:         "Synthetic track-insert breaks Apple Devices sync",
		Description:        fmt.Sprintf("Baseline + %d synthetic track(s) inserted via AddTracksLE (the production write path).", n),
		BaselineDerived:    true,
		Mutations:          mut,
		ExpectedTrackDelta: n,
	}, nil
}

func diagAddSyntheticTracks(dir string, baseline []byte, n int, deterministic bool) (SyncDiagInfo, error) {
	out, info, err := diagAddSynthetic(baseline, n, deterministic)
	if err != nil {
		return SyncDiagInfo{}, err
	}
	info.ID = filepath.Base(dir)
	if err := os.WriteFile(filepath.Join(dir, "iTunes Library.itl"), out, 0o664); err != nil {
		return SyncDiagInfo{}, err
	}
	return info, nil
}

func diagAddSyntheticAt(dir string, baseline []byte, location, extra string) (SyncDiagInfo, error) {
	hdr, _ := parseHdfmHeader(baseline)
	payload := baseline[hdr.headerLen:]
	decrypted := itlDecrypt(hdr, payload)
	decompressed, wasCompressed, err := itlInflate(decrypted)
	if err != nil {
		return SyncDiagInfo{}, fmt.Errorf("decompressing: %w", err)
	}

	tr := ITLNewTrack{
		Location: location,
		Name:     "Diag Real-File Track",
		Album:    "Diag Real-File Album",
		Artist:   "Diag Real-File Author",
		Genre:    "Audiobook",
		Kind:     "AAC audio file",
	}
	modified := AddTracksLE(decompressed, []ITLNewTrack{tr})
	if err := writeITLFileRaw(filepath.Join(dir, "iTunes Library.itl"), hdr, modified, wasCompressed); err != nil {
		return SyncDiagInfo{}, err
	}
	desc := fmt.Sprintf("Baseline + 1 synthetic track pointing at Location=%q.", location)
	if extra != "" {
		desc += " " + extra
	}
	return SyncDiagInfo{
		ID:                 filepath.Base(dir),
		Hypothesis:         "H23: file at Location must exist on Windows for sync to proceed",
		Description:        desc,
		BaselineDerived:    true,
		Mutations:          []string{"AddTracksLE", "location_real_file"},
		ExpectedTrackDelta: 1,
	}, nil
}

func diagAddVariant(dir string, baseline []byte, mutTag, hypo string, tr ITLNewTrack, deterministic bool) (SyncDiagInfo, error) {
	hdr, _ := parseHdfmHeader(baseline)
	payload := baseline[hdr.headerLen:]
	decrypted := itlDecrypt(hdr, payload)
	decompressed, wasCompressed, err := itlInflate(decrypted)
	if err != nil {
		return SyncDiagInfo{}, fmt.Errorf("decompressing: %w", err)
	}
	modified := AddTracksLE(decompressed, []ITLNewTrack{tr})
	if err := writeITLFileRaw(filepath.Join(dir, "iTunes Library.itl"), hdr, modified, wasCompressed); err != nil {
		return SyncDiagInfo{}, err
	}
	desc := fmt.Sprintf("Baseline + 1 track. Variant: %s. Track JSON: %s", mutTag, mustJSON(tr))
	return SyncDiagInfo{
		ID:                 filepath.Base(dir),
		Hypothesis:         hypo,
		Description:        desc,
		BaselineDerived:    true,
		Mutations:          []string{"AddTracksLE", mutTag},
		ExpectedTrackDelta: 1,
	}, nil
}

func diagCloneFirstBaselineTrack(dir string, baseline []byte) (SyncDiagInfo, error) {
	// Parse the baseline, find the first track's PID, ask AddTracksLE to
	// add a track with the SAME basic shape but a fresh PID and a new
	// title. This is meant to look as similar as possible to a real
	// baseline mith — our writer can't produce identical mith bytes today,
	// so this is still synthetic but uses one baseline-track's metadata as
	// a template.
	lib, err := ParseITLBytes(baseline)
	if err != nil {
		return SyncDiagInfo{}, err
	}
	if len(lib.Tracks) == 0 {
		return SyncDiagInfo{}, fmt.Errorf("baseline has no tracks to clone")
	}
	src := lib.Tracks[0]
	tr := ITLNewTrack{
		Location:    src.Location,
		Name:        src.Name + " (DIAG CLONE)",
		Album:       src.Album,
		Artist:      src.Artist,
		Genre:       src.Genre,
		Kind:        src.Kind,
		Size:        src.Size,
		TotalTime:   src.TotalTime,
		TrackNumber: src.TrackNumber,
		DiscNumber:  src.DiscNumber,
		BitRate:     src.BitRate,
		SampleRate:  src.SampleRate,
		Year:        src.Year,
	}
	hdr, _ := parseHdfmHeader(baseline)
	payload := baseline[hdr.headerLen:]
	decrypted := itlDecrypt(hdr, payload)
	decompressed, wasCompressed, err := itlInflate(decrypted)
	if err != nil {
		return SyncDiagInfo{}, fmt.Errorf("decompressing: %w", err)
	}
	modified := AddTracksLE(decompressed, []ITLNewTrack{tr})
	if err := writeITLFileRaw(filepath.Join(dir, "iTunes Library.itl"), hdr, modified, wasCompressed); err != nil {
		return SyncDiagInfo{}, err
	}
	return SyncDiagInfo{
		ID:                 "38-aborg-clone-baseline-track",
		Hypothesis:         "Clone an existing baseline track via AddTracksLE — closest synthetic to baseline",
		Description:        fmt.Sprintf("Cloned track 1 (%q) with new title; same Location/Album/Artist/Genre/Kind.", src.Name),
		BaselineDerived:    true,
		Mutations:          []string{"AddTracksLE", "clone_baseline_track_0"},
		ExpectedTrackDelta: 1,
	}, nil
}

func diagLocationUpdateOnly(dir string, baseline []byte, baselinePath string) (SyncDiagInfo, error) {
	// Use UpdateITLLocations against the baseline, supplying an update
	// for a PID that won't match anything (so we exercise the write-back
	// pipeline without actually editing any track).
	out := filepath.Join(dir, "iTunes Library.itl")
	updates := []ITLLocationUpdate{
		{PersistentID: "0000000000000000", NewLocation: "file://localhost/W:/diag/never-matches.m4b"},
	}
	res, err := UpdateITLLocations(baselinePath, out, updates)
	if err != nil {
		return SyncDiagInfo{}, err
	}
	return SyncDiagInfo{
		ID:              filepath.Base(dir),
		Hypothesis:      "Write-back pipeline corrupts library even when no tracks change",
		Description:     fmt.Sprintf("Ran UpdateITLLocations against baseline with a PID-update that matches nothing. UpdatedCount=%d. The decrypt/recompress/encrypt pipeline runs in full.", res.UpdatedCount),
		BaselineDerived: true,
		Mutations:       []string{"UpdateITLLocations:no-op-update"},
	}, nil
}

func diagDocStub(dir string, baseline []byte, id, hypo, desc string) (SyncDiagInfo, error) {
	if err := os.WriteFile(filepath.Join(dir, "iTunes Library.itl"), baseline, 0o664); err != nil {
		return SyncDiagInfo{}, err
	}
	return SyncDiagInfo{
		ID:                     id,
		Hypothesis:             hypo,
		Description:            desc + " (Variant ITL is a baseline copy until format research lands.)",
		BaselineDerived:        true,
		Mutations:              []string{"baseline-copy", "format-research-required"},
		RequiresFormatResearch: true,
	}, nil
}

// ---------------------------------------------------------------------------
// Glue helpers
// ---------------------------------------------------------------------------

// writeITLBytes is a sibling of writeITLFileRaw that writes to a buffer
// instead of disk.
func writeITLBytes(out *[]byte, hdr *hdfmHeader, payload []byte, compress bool) error {
	var finalPayload []byte
	if compress {
		finalPayload = itlDeflate(payload)
	} else {
		finalPayload = payload
	}
	encrypted := itlEncrypt(hdr, finalPayload)
	newFileLen := uint32(len(encrypted)) + hdr.headerLen
	newHeader := buildHdfmHeader(hdr.version, hdr.headerRemainder, newFileLen, hdr.unknown)
	*out = append(*out, newHeader...)
	*out = append(*out, encrypted...)
	return nil
}

// ParseITLBytes is a convenience wrapper that parses an ITL from a byte
// slice (instead of a file).
func ParseITLBytes(data []byte) (*ITLLibrary, error) {
	tmp, err := os.CreateTemp("", "diag-baseline-*.itl")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return nil, err
	}
	tmp.Close()
	return ParseITL(tmp.Name())
}

func writeOut(dir string, data []byte, info SyncDiagInfo) (SyncDiagInfo, error) {
	info.ID = filepath.Base(dir)
	if err := os.WriteFile(filepath.Join(dir, "iTunes Library.itl"), data, 0o664); err != nil {
		return SyncDiagInfo{}, err
	}
	return info, nil
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o664)
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func writeReadme(dir string, n int) error {
	body := strings.ReplaceAll(syncDiagReadmeTemplate, "{{N}}", fmt.Sprintf("%d", n))
	return os.WriteFile(filepath.Join(dir, "README.md"), []byte(body), 0o664)
}

const syncDiagReadmeTemplate = `# iTunes / Apple Devices Sync Diagnostic Suite

Generated by audiobook-organizer ` + "`cmd/itunes-sync-tests`" + `.

This folder contains {{N}} variant iTunes Library.itl files. Each
` + "`NN-name/`" + ` subfolder has:

- ` + "`iTunes Library.itl`" + ` — the variant
- ` + "`test-info.json`" + ` — describes the hypothesis and what's mutated
- ` + "`result.json`" + ` — fill in after manually testing on Windows

` + "`index.json`" + ` lists every variant.

## How to use on Windows

1. Back up your real ` + "`%USERPROFILE%\\Music\\iTunes\\iTunes Library.itl`" + ` ONCE.
2. For each test folder ` + "`NN-name/`" + `:
   - Close iTunes and Apple Devices.
   - Copy ` + "`iTunes Library.itl`" + ` into ` + "`%USERPROFILE%\\Music\\iTunes\\`" + `.
   - Open iTunes — record whether it opens cleanly.
   - Plug in iPhone, open Apple Devices, attempt a sync of Audiobooks ONLY.
   - Update ` + "`result.json`" + `: set ` + "`opens_in_itunes`" + ` (yes/no),
     ` + "`apple_devices_sync`" + ` (worked/failed/skipped), and any
     ` + "`notes`" + ` text.

## How to interpret

- If 00-baseline-untouched FAILS sync: the bug is environmental (Apple
  Devices version, iPhone state, Apple Music interaction) — not in the
  audiobook-organizer writer.
- If 01-roundtrip-only FAILS but 00 works: our decrypt/encrypt pipeline
  introduces corruption.
- If 13-baseline-minus-1 FAILS but 01 works: ` + "`RemoveLastNTracksLE`" + `
  damages something.
- If 39 / 40 (location-update-only on baseline) FAIL but 01 works: our
  location-update mutator damages something.
- If 15 (one synthetic track added) FAILS but 38 (clone existing track)
  works: our synthetic mith builder is missing fields the real one has.
- The 18-26 mhoh variants narrow which metadata field matters.
- The 32-36 mith-field variants need follow-up format research.
`
