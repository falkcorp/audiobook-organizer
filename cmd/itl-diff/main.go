// file: cmd/itl-diff/main.go
// version: 2.0.0
// guid: 9d0e1f2a-3b4c-5d6e-7f8a-9b0c1d2e3f4a
//
// Structural diff between two iTunes Library.itl files. Reports:
//   - hdfm header comparison (version, header length, file length; byte-diff
//     of the first 96 raw bytes)
//   - msdh container inventory (block-type → hdrLen/totalLen, Δ between A and B)
//   - track count deltas and per-track metadata diff (by Persistent ID); string
//     fields are meaningful when T001's mhoh-descent fix is present
//   - playlist membership diff (per-playlist added/removed TIDs)
//   - optional: --audit runs AuditITL on both sides and reports per-guard results
//
// Usage: itl-diff [flags] <a.itl> <b.itl>

package main

import (
	"bytes"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/falkcorp/audiobook-organizer/internal/itunes"
)

func main() {
	verbose := flag.Bool("v", false, "Verbose: list per-track diffs")
	max := flag.Int("max", 20, "Max per-track diffs to print in verbose mode")
	audit := flag.Bool("audit", false, "Run AuditITL (ITLSafetyContract) on both libraries")
	flag.Parse()
	if flag.NArg() != 2 {
		fmt.Fprintln(os.Stderr, "Usage: itl-diff [-v] [-max N] [--audit] <a.itl> <b.itl>")
		os.Exit(2)
	}

	a, err := itunes.ParseITL(flag.Arg(0))
	must(err, "parse A")
	b, err := itunes.ParseITL(flag.Arg(1))
	must(err, "parse B")

	fmt.Printf("A: %s\nB: %s\n\n", flag.Arg(0), flag.Arg(1))

	// File bytes for low-level comparison.
	rawA, _ := os.ReadFile(flag.Arg(0))
	rawB, _ := os.ReadFile(flag.Arg(1))
	fmt.Printf("File size:        A=%d  B=%d  (Δ=%+d)\n",
		len(rawA), len(rawB), len(rawB)-len(rawA))
	fmt.Printf("ITL version:      A=%q B=%q\n", a.Version, b.Version)
	fmt.Printf("Track count:      A=%d B=%d (Δ=%+d)\n",
		len(a.Tracks), len(b.Tracks), len(b.Tracks)-len(a.Tracks))
	fmt.Printf("Playlist count:   A=%d B=%d (Δ=%+d)\n\n",
		len(a.Playlists), len(b.Playlists), len(b.Playlists)-len(a.Playlists))

	// hdfm header hex diff.
	if len(rawA) >= 96 && len(rawB) >= 96 {
		fmt.Println("hdfm header (first 96 bytes):")
		printHexDiff(rawA[:96], rawB[:96])
		fmt.Println()
	}

	// msdh container inventory diff.
	printInventoryDiff(rawA, rawB)

	// Per-track diff by PID.
	indexA := map[string]itunes.ITLTrack{}
	for _, t := range a.Tracks {
		indexA[hex.EncodeToString(t.PersistentID[:])] = t
	}
	indexB := map[string]itunes.ITLTrack{}
	for _, t := range b.Tracks {
		indexB[hex.EncodeToString(t.PersistentID[:])] = t
	}

	var onlyA, onlyB, changed []string
	for pid, ta := range indexA {
		tb, ok := indexB[pid]
		if !ok {
			onlyA = append(onlyA, pid)
			continue
		}
		if !tracksEqual(ta, tb) {
			changed = append(changed, pid)
		}
	}
	for pid := range indexB {
		if _, ok := indexA[pid]; !ok {
			onlyB = append(onlyB, pid)
		}
	}
	sort.Strings(onlyA)
	sort.Strings(onlyB)
	sort.Strings(changed)

	fmt.Printf("Tracks only in A:  %d\n", len(onlyA))
	fmt.Printf("Tracks only in B:  %d\n", len(onlyB))
	fmt.Printf("Tracks changed:    %d\n", len(changed))

	if *verbose {
		printList("only-in-A", onlyA, indexA, *max)
		printList("only-in-B", onlyB, indexB, *max)
		fmt.Println()
		fmt.Printf("Per-track diffs (first %d):\n", *max)
		for i, pid := range changed {
			if i >= *max {
				fmt.Printf("...and %d more\n", len(changed)-*max)
				break
			}
			ta := indexA[pid]
			tb := indexB[pid]
			fmt.Printf("\n  PID %s:\n", pid)
			diffField("Name", ta.Name, tb.Name)
			diffField("Album", ta.Album, tb.Album)
			diffField("Artist", ta.Artist, tb.Artist)
			diffField("Genre", ta.Genre, tb.Genre)
			diffField("Kind", ta.Kind, tb.Kind)
			diffField("Location", ta.Location, tb.Location)
			diffInt("Size", ta.Size, tb.Size)
			diffInt("TotalTime", ta.TotalTime, tb.TotalTime)
			diffInt("BitRate", ta.BitRate, tb.BitRate)
			diffInt("SampleRate", ta.SampleRate, tb.SampleRate)
			diffInt("PlayCount", ta.PlayCount, tb.PlayCount)
			diffInt("Rating", ta.Rating, tb.Rating)
			diffInt("Year", ta.Year, tb.Year)
		}
	}

	// Playlist membership diff.
	fmt.Println()
	printMembershipDiff(a, b, *verbose, *max)

	// AuditITL on both sides (--audit flag).
	if *audit {
		fmt.Println()
		printAuditSection("A", rawA)
		printAuditSection("B", rawB)
	}
}

// printInventoryDiff prints the msdh container inventory comparison.
func printInventoryDiff(rawA, rawB []byte) {
	payloadA, errA := itunes.DecryptAndInflateITL(rawA)
	payloadB, errB := itunes.DecryptAndInflateITL(rawB)

	fmt.Println("msdh container inventory:")
	if errA != nil || errB != nil {
		if errA != nil {
			fmt.Printf("  (cannot decode A: %v)\n", errA)
		}
		if errB != nil {
			fmt.Printf("  (cannot decode B: %v)\n", errB)
		}
		fmt.Println()
		return
	}

	invA := itunes.CollectMsdhInventory(payloadA)
	invB := itunes.CollectMsdhInventory(payloadB)

	// Print per-container table.
	indexB := make(map[int]itunes.MsdhEntry, len(invB))
	for _, e := range invB {
		indexB[e.BlockType] = e
	}
	indexA := make(map[int]itunes.MsdhEntry, len(invA))
	for _, e := range invA {
		indexA[e.BlockType] = e
	}

	// Collect all known block types.
	allTypes := map[int]struct{}{}
	for _, e := range invA {
		allTypes[e.BlockType] = struct{}{}
	}
	for _, e := range invB {
		allTypes[e.BlockType] = struct{}{}
	}
	sortedTypes := make([]int, 0, len(allTypes))
	for bt := range allTypes {
		sortedTypes = append(sortedTypes, bt)
	}
	sort.Ints(sortedTypes)

	fmt.Printf("  %-14s  %-12s  %-12s  %-12s  %-12s  %s\n",
		"type", "A.hdrLen", "A.totalLen", "B.hdrLen", "B.totalLen", "delta")
	for _, bt := range sortedTypes {
		ea, okA := indexA[bt]
		eb, okB := indexB[bt]
		name := itunes.MsdhEntry{BlockType: bt}.BlockTypeName()
		label := fmt.Sprintf("%d (%s)", bt, name)
		if !okA {
			fmt.Printf("  %-14s  %-12s  %-12s  %-12d  %-12d  (only in B)\n",
				label, "-", "-", eb.HeaderLen, eb.TotalLen)
		} else if !okB {
			fmt.Printf("  %-14s  %-12d  %-12d  %-12s  %-12s  (only in A)\n",
				label, ea.HeaderLen, ea.TotalLen, "-", "-")
		} else {
			delta := eb.TotalLen - ea.TotalLen
			marker := " "
			if delta != 0 {
				marker = "*"
			}
			fmt.Printf("  %s %-13s  %-12d  %-12d  %-12d  %-12d  %+d\n",
				marker, label, ea.HeaderLen, ea.TotalLen, eb.HeaderLen, eb.TotalLen, delta)
		}
	}
	fmt.Println()
}

// printMembershipDiff prints playlist membership changes.
func printMembershipDiff(a, b *itunes.ITLLibrary, verbose bool, maxItems int) {
	result := itunes.DiffPlaylistMembership(a, b)

	fmt.Printf("Playlist membership diff:\n")
	fmt.Printf("  Playlists only in A: %d\n", len(result.OnlyA))
	fmt.Printf("  Playlists only in B: %d\n", len(result.OnlyB))
	fmt.Printf("  Playlists changed:   %d\n", len(result.Changed))

	if !verbose || len(result.Changed) == 0 {
		return
	}
	fmt.Println()
	for i, ch := range result.Changed {
		if i >= maxItems {
			fmt.Printf("  ...and %d more changed playlists\n", len(result.Changed)-maxItems)
			break
		}
		title := ch.Title
		if title == "" {
			title = "(untitled)"
		}
		fmt.Printf("  Playlist %s %q:\n", ch.PID, title)
		if len(ch.Added) > 0 {
			fmt.Printf("    Added   TIDs: %v\n", ch.Added)
		}
		if len(ch.Removed) > 0 {
			fmt.Printf("    Removed TIDs: %v\n", ch.Removed)
		}
	}
}

// printAuditSection runs AuditITL on a raw library and prints the per-guard verdict.
func printAuditSection(label string, raw []byte) {
	fmt.Printf("AuditITL (%s):\n", label)
	verdict := itunes.AuditITL(raw)
	for _, r := range verdict.Results {
		status := "PASS"
		if !r.Pass() {
			status = fmt.Sprintf("FAIL (%d violation(s))", len(r.Violations))
		}
		fmt.Printf("  %-24s  %s\n", r.Guard, status)
	}
	if verdict.Pass {
		fmt.Printf("  -> %s: all guards passed\n", label)
	} else {
		fmt.Printf("  -> %s: VIOLATIONS in guards: %v\n", label, verdict.FailedGuards())
	}
	fmt.Println()
}

func tracksEqual(a, b itunes.ITLTrack) bool {
	return a.Name == b.Name && a.Album == b.Album && a.Artist == b.Artist &&
		a.Genre == b.Genre && a.Kind == b.Kind && a.Location == b.Location &&
		a.Size == b.Size && a.TotalTime == b.TotalTime &&
		a.BitRate == b.BitRate && a.SampleRate == b.SampleRate &&
		a.PlayCount == b.PlayCount && a.Rating == b.Rating && a.Year == b.Year
}

func diffField(name, a, b string) {
	if a != b {
		fmt.Printf("    %-12s A=%q B=%q\n", name, a, b)
	}
}

func diffInt(name string, a, b int) {
	if a != b {
		fmt.Printf("    %-12s A=%d B=%d\n", name, a, b)
	}
}

func printList(label string, pids []string, idx map[string]itunes.ITLTrack, max int) {
	if len(pids) == 0 {
		return
	}
	fmt.Printf("\n%s (showing up to %d):\n", label, max)
	for i, pid := range pids {
		if i >= max {
			fmt.Printf("  ...and %d more\n", len(pids)-max)
			break
		}
		t := idx[pid]
		fmt.Printf("  %s  %q  by %q\n", pid, t.Name, t.Artist)
	}
}

func printHexDiff(a, b []byte) {
	n := min(len(b), len(a))
	for i := 0; i < n; i += 16 {
		end := min(i+16, n)
		ra := a[i:end]
		rb := b[i:end]
		marker := " "
		if !bytes.Equal(ra, rb) {
			marker = "*"
		}
		fmt.Printf("  %s %04x  A: %-48s | B: %-48s\n", marker, i,
			hex.EncodeToString(ra), hex.EncodeToString(rb))
	}
}

func must(err error, what string) {
	if err != nil {
		fmt.Fprintln(os.Stderr, what+":", err)
		os.Exit(1)
	}
}
