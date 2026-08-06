// file: internal/linkintegrity/classify_test.go
// version: 1.0.0
// guid: b93f206e-71cd-4a58-8e04-3d1927fa6c85
// last-edited: 2026-08-05

package linkintegrity

import "testing"

// TestClassifyDirRealFolders uses folder contents observed on production on
// 2026-08-05. These are the cases that motivated owner decision D1 — they are
// indistinguishable to a naive "link every file in the folder" rule, and getting
// any of the MANY-book cases wrong merges distinct novels into one row.
func TestClassifyDirRealFolders(t *testing.T) {
	const hr = 3600
	tests := []struct {
		name      string
		files     []string
		subdirs   int
		durations []int
		wantOne   bool
	}{
		{
			// Emberverse/Book 10 - The Given Sacrifice — 23 chapter files.
			name: "chapter files of one book",
			files: []string{
				"The Given Sacrifice_01_.mp3", "The Given Sacrifice_02_.mp3",
				"The Given Sacrifice_03_.mp3", "The Given Sacrifice_04_.mp3",
			},
			durations: []int{1500, 1480, 1520, 1495},
			wantOne:   true,
		},
		{
			// Faith Hunter (Jane Yellowrock) — 11 files, DIFFERENT novels.
			name: "different novels sharing a folder",
			files: []string{
				"01 Skinwalker part 1.mp3", "01 Skinwalker part 2.mp3",
				"02 Blood Cross Part 1.mp3", "02 Blood Cross Part 2.mp3",
			},
			durations: []int{4 * hr, 4 * hr, 4 * hr, 4 * hr},
			wantOne:   false,
		},
		{
			// Doctor Who/Spin-offs/Stageplays — 5 different plays.
			name: "different works, one per file",
			files: []string{
				"ST02 The Seven Keys to Doomsday.m4b",
				"ST03 - Curse of the Daleks.m4b",
				"ST1-01 The Ultimate Adventure Act I.m4b",
			},
			durations: []int{2 * hr, 2 * hr, 2 * hr},
			wantOne:   false,
		},
		{
			// The Hatching — Part01..Part08 of ONE book.
			name: "part markers of one book",
			files: []string{
				"The Hatching-Part01.mp3", "The Hatching-Part02.mp3",
				"The Hatching-Part03.mp3",
			},
			durations: []int{2400, 2350, 2410},
			wantOne:   true,
		},
		{
			// The series shape stems alone cannot separate: one stem, but each
			// member is a whole book. This is the "Super Sales on Super Heroes"
			// near-miss that duration — and only duration — catches.
			name: "one stem but members are book-length",
			files: []string{
				"Sentenced to Troll 1.m4b", "Sentenced to Troll 2.m4b",
				"Sentenced to Troll 3.m4b",
			},
			durations: []int{9 * hr, 10 * hr, 8 * hr},
			wantOne:   false,
		},
		{
			name:      "single audio file",
			files:     []string{"The Rising.m4b"},
			durations: []int{9 * hr},
			wantOne:   true,
		},
		{
			name:    "no audio but subdirectories",
			files:   []string{"Disc 1", "Disc 2"},
			subdirs: 2,
			wantOne: false,
		},
		{
			name:    "empty folder",
			files:   []string{"cover.jpg", "notes.txt"},
			wantOne: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyDir(tc.files, tc.subdirs, tc.durations)
			if got.OneBook != tc.wantOne {
				t.Errorf("OneBook = %v, want %v\n  reason: %s\n  stems: %d, audio: %d",
					got.OneBook, tc.wantOne, got.Reason, got.DistinctStems, got.AudioCount)
			}
			if got.Reason == "" {
				t.Error("Reason must never be empty — a queue of blank reasons is unworkable")
			}
		})
	}
}

// TestClassifyDirRefusesWithoutDurations: durations are the only signal that
// separates a chapter set from a series sharing one title. Without them the
// classifier must REFUSE rather than guess — 97.5% of the review queue had no
// durations, and guessing is what produced the 41-of-43 near-miss.
func TestClassifyDirRefusesWithoutDurations(t *testing.T) {
	files := []string{"Book A 1.mp3", "Book A 2.mp3", "Book A 3.mp3"}
	got := ClassifyDir(files, 0, nil)
	if got.OneBook {
		t.Errorf("must not auto-link a multi-file folder with unknown durations; reason=%q", got.Reason)
	}
	// With durations, the same folder resolves.
	got = ClassifyDir(files, 0, []int{1200, 1300, 1250})
	if !got.OneBook {
		t.Errorf("chapter-length members should link; reason=%q", got.Reason)
	}
}

func TestTitleStem(t *testing.T) {
	tests := []struct {
		a, b string
		same bool
	}{
		{"The Hatching-Part01.mp3", "The Hatching-Part08.mp3", true},
		{"The Given Sacrifice_01_.mp3", "The Given Sacrifice_23_.mp3", true},
		{"01 - The Empty Place.mp3", "02 - The Phalangite Ascendancy.mp3", false},
		{"01 Skinwalker part 1.mp3", "02 Blood Cross Part 1.mp3", false},
		{"Magicians Land Part 1of2.mp3", "Magicians Land Part 2of2.mp3", true},
		{"Dune (Unabridged) CD 1.mp3", "Dune (Unabridged) CD 2.mp3", true},
	}
	for _, tc := range tests {
		sa, sb := TitleStem(tc.a), TitleStem(tc.b)
		if (sa == sb) != tc.same {
			t.Errorf("TitleStem(%q)=%q vs TitleStem(%q)=%q — same=%v, want %v",
				tc.a, sa, tc.b, sb, sa == sb, tc.same)
		}
	}
}

func TestIsAudioFile(t *testing.T) {
	for _, n := range []string{"a.mp3", "b.M4B", "c.flac", "d.opus"} {
		if !IsAudioFile(n) {
			t.Errorf("IsAudioFile(%q) = false, want true", n)
		}
	}
	// Extensionless names are the directory-shaped case — must NOT count as audio.
	for _, n := range []string{"The First Law 01 The Blade Itself", "cover.jpg", "audiobooks"} {
		if IsAudioFile(n) {
			t.Errorf("IsAudioFile(%q) = true, want false", n)
		}
	}
}
