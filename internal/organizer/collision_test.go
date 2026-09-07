// file: internal/organizer/collision_test.go
// version: 1.2.0
// guid: 4e07b5c9-1a26-4f83-b0d7-92c1e6438af5
// last-edited: 2026-09-07

package organizer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// ---------------------------------------------------------------------------
// fakes
// ---------------------------------------------------------------------------

type fakeCollisionStore struct {
	byPath     map[string]*database.BookFile
	byID       map[string]*database.BookFile
	updates    int
	failUpdate bool
}

func newFakeCollisionStore() *fakeCollisionStore {
	return &fakeCollisionStore{
		byPath: map[string]*database.BookFile{},
		byID:   map[string]*database.BookFile{},
	}
}

func (f *fakeCollisionStore) add(bf *database.BookFile) {
	cp := *bf
	f.byID[bf.ID] = &cp
	f.byPath[bf.FilePath] = &cp
}

func (f *fakeCollisionStore) GetBookFileByPath(p string) (*database.BookFile, error) {
	if bf, ok := f.byPath[p]; ok {
		cp := *bf
		return &cp, nil
	}
	return nil, nil
}

func (f *fakeCollisionStore) GetBookFileByID(_, fileID string) (*database.BookFile, error) {
	if bf, ok := f.byID[fileID]; ok {
		cp := *bf
		return &cp, nil
	}
	return nil, fmt.Errorf("no such book_file %s", fileID)
}

func (f *fakeCollisionStore) UpdateBookFile(id string, file *database.BookFile) error {
	if f.failUpdate {
		return errors.New("injected update failure")
	}
	f.updates++
	old, ok := f.byID[id]
	if ok {
		delete(f.byPath, old.FilePath)
	}
	cp := *file
	f.byID[id] = &cp
	f.byPath[cp.FilePath] = &cp
	return nil
}

// fakePrefStore is the `_system` preference namespace the durable-failure
// record lives in. It mirrors the production convention that "cleared" is an
// EMPTY value rather than a deleted row.
type fakePrefStore struct {
	kv map[string]string
}

func newFakePrefStore() *fakePrefStore { return &fakePrefStore{kv: map[string]string{}} }

func (f *fakePrefStore) GetUserPreference(string) (*database.UserPreference, error) { return nil, nil }
func (f *fakePrefStore) SetUserPreference(string, string) error                     { return nil }
func (f *fakePrefStore) DeleteUserPreference(string) error                          { return nil }
func (f *fakePrefStore) GetAllUserPreferences() ([]database.UserPreference, error)  { return nil, nil }

func (f *fakePrefStore) SetUserPreferenceForUser(userID, key, value string) error {
	f.kv[userID+"\x00"+key] = value
	return nil
}

func (f *fakePrefStore) GetUserPreferenceForUser(userID, key string) (*database.UserPreferenceKV, error) {
	v, ok := f.kv[userID+"\x00"+key]
	if !ok {
		return nil, nil
	}
	return &database.UserPreferenceKV{UserID: userID, Key: key, Value: v}, nil
}

func (f *fakePrefStore) GetAllPreferencesForUser(userID string) ([]database.UserPreferenceKV, error) {
	var out []database.UserPreferenceKV
	for k, v := range f.kv {
		parts := strings.SplitN(k, "\x00", 2)
		if parts[0] != userID {
			continue
		}
		out = append(out, database.UserPreferenceKV{UserID: userID, Key: parts[1], Value: v})
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func writeCollisionFile(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o775); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func mustCollisionExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("expected %s to exist: %v", path, err)
	}
}

func mustCollisionNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); err == nil {
		t.Fatalf("expected %s to be gone", path)
	}
}

func readCollisionFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// ---------------------------------------------------------------------------
// identity ladder
// ---------------------------------------------------------------------------

// TestClassifyCheap_IdentityLadder walks the ladder rung by rung. Each case
// names the rung that MUST decide it, so a change that reorders the ladder (for
// example hashing first) shows up as a test failure rather than as a silent
// performance regression on multi-GB files.
func TestClassifyCheap_IdentityLadder(t *testing.T) {
	dir := t.TempDir()

	plain := writeCollisionFile(t, filepath.Join(dir, "plain.m4b"), "aaaa")
	hardlink := filepath.Join(dir, "hardlink.m4b")
	if err := os.Link(plain, hardlink); err != nil {
		t.Skipf("filesystem has no hard links: %v", err)
	}
	sameSizeOther := writeCollisionFile(t, filepath.Join(dir, "other.m4b"), "bbbb")
	bigger := writeCollisionFile(t, filepath.Join(dir, "bigger.m4b"), "aaaaaaaa")

	stat := func(p string) os.FileInfo {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		return fi
	}

	cases := []struct {
		name    string
		src     string
		dst     string
		srcHash string
		dstHash string
		want    identityVerdict
		rung    string
	}{
		{
			name: "same inode (hardlink) decided by os.SameFile before any read",
			src:  plain, dst: hardlink, want: identityIdentical, rung: "os.SameFile",
		},
		{
			name: "size mismatch is a fast reject",
			src:  plain, dst: bigger, want: identityDifferent, rung: "size",
		},
		{
			name: "same size, different content, no stored hashes: undecided so the SHA rung runs",
			src:  plain, dst: sameSizeOther, want: identityUnknown, rung: "none",
		},
		{
			name: "same size, matching stored hashes: identical with no file read",
			src:  plain, dst: sameSizeOther, srcHash: "deadbeef", dstHash: "deadbeef",
			want: identityIdentical, rung: "stored FileHash",
		},
		{
			name: "same size, disagreeing stored hashes: different with no file read",
			src:  plain, dst: sameSizeOther, srcHash: "deadbeef", dstHash: "cafebabe",
			want: identityDifferent, rung: "stored FileHash",
		},
		{
			name: "one stored hash missing falls through to the SHA rung",
			src:  plain, dst: sameSizeOther, srcHash: "deadbeef",
			want: identityUnknown, rung: "none",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyCheap(stat(tc.src), stat(tc.dst), tc.srcHash, tc.dstHash)
			if got != tc.want {
				t.Fatalf("classifyCheap = %v, want %v (rung under test: %s)", got, tc.want, tc.rung)
			}
		})
	}
}

// TestHashUndecided_ResolvesSameSizeDifferentContent proves the last rung
// actually runs and separates two same-size files.
func TestHashUndecided_ResolvesSameSizeDifferentContent(t *testing.T) {
	dir := t.TempDir()
	a := writeCollisionFile(t, filepath.Join(dir, "a.m4b"), "aaaa")
	b := writeCollisionFile(t, filepath.Join(dir, "b.m4b"), "bbbb")
	same := writeCollisionFile(t, filepath.Join(dir, "same.m4b"), "aaaa")

	mk := func(src, dst string) collisionCandidate {
		return collisionCandidate{entry: FileRenameEntry{SourcePath: src, TargetPath: dst}}
	}
	cands := []collisionCandidate{mk(a, b), mk(a, same)}
	if err := hashUndecided(cands, &CollisionPolicy{}); err != nil {
		t.Fatalf("hashUndecided: %v", err)
	}
	if cands[0].verdict != identityDifferent {
		t.Fatalf("different content: got %v", cands[0].verdict)
	}
	if cands[1].verdict != identityIdentical {
		t.Fatalf("same content: got %v", cands[1].verdict)
	}
}

// TestHashUndecided_NeverHashesADecidedPair is the guard for the "never hash
// when a cheaper rung already decided" rule: the hasher is instrumented and
// must not be called at all.
func TestHashUndecided_NeverHashesADecidedPair(t *testing.T) {
	called := 0
	policy := &CollisionPolicy{hashFile: func(string) (string, error) {
		called++
		return "", nil
	}}
	cands := []collisionCandidate{
		{verdict: identityIdentical},
		{verdict: identityDifferent},
	}
	if err := hashUndecided(cands, policy); err != nil {
		t.Fatalf("hashUndecided: %v", err)
	}
	if called != 0 {
		t.Fatalf("hasher ran %d times for pairs the cheap rungs already decided", called)
	}
}

// ---------------------------------------------------------------------------
// branch fixtures
// ---------------------------------------------------------------------------

// TestRenameFiles_InLibraryIdentical_QuarantinesSourceAndRepoints is the prod
// case: the occupant is byte-identical and already sits at the organized path,
// so it wins; our copy is quarantined (never unlinked) and the row follows the
// kept file.
func TestRenameFiles_InLibraryIdentical_QuarantinesSourceAndRepoints(t *testing.T) {
	root := t.TempDir()
	src := writeCollisionFile(t, filepath.Join(root, "incoming", "book.m4b"), "identical-bytes")
	dst := writeCollisionFile(t, filepath.Join(root, "Author", "Title.m4b"), "identical-bytes")

	store := newFakeCollisionStore()
	store.add(&database.BookFile{ID: "f1", BookID: "b1", FilePath: src, FileSize: 15})

	result, err := RenameFiles(
		[]FileRenameEntry{{SegmentID: "f1", SourcePath: src, TargetPath: dst, ExpectedSize: 15}},
		&CollisionPolicy{RootDir: root, BookID: "b1", Store: store},
	)
	if err != nil {
		t.Fatalf("RenameFiles: %v", err)
	}

	if len(result.Resolutions) != 1 || result.Resolutions[0].Action != CollisionQuarantinedSource {
		t.Fatalf("expected one quarantined-source resolution, got %+v", result.Resolutions)
	}
	// The occupant is untouched and still holds its bytes.
	if got := readCollisionFile(t, dst); got != "identical-bytes" {
		t.Fatalf("occupant was modified: %q", got)
	}
	// Our source moved, it was NOT deleted, and it landed under .failed.
	mustCollisionNotExist(t, src)
	q := result.Resolutions[0].QuarantinePath
	mustCollisionExist(t, q)
	if !strings.HasPrefix(q, filepath.Join(root, ".failed")) {
		t.Fatalf("quarantine path %s is not under the .failed tree", q)
	}
	if strings.HasSuffix(q, ".tmp") {
		t.Fatalf("quarantine name %s ends in .tmp and would be swept as scratch", q)
	}
	// The row was REPOINTED at the kept file, not deleted.
	row, _ := store.GetBookFileByID("b1", "f1")
	if row == nil || row.FilePath != dst {
		t.Fatalf("row was not repointed at the kept file: %+v", row)
	}
	if row.Missing {
		t.Fatalf("row must not be tombstoned when it is the surviving row")
	}
}

// TestRenameFiles_InLibraryIdentical_SameBookSiblingRowTombstones covers the
// one case where a row IS tombstoned: another row of the SAME book already
// points at the kept file, so repointing ours there too would manufacture a
// duplicate row for one path.
func TestRenameFiles_InLibraryIdentical_SameBookSiblingRowTombstones(t *testing.T) {
	root := t.TempDir()
	src := writeCollisionFile(t, filepath.Join(root, "incoming", "book.m4b"), "identical-bytes")
	dst := writeCollisionFile(t, filepath.Join(root, "Author", "Title.m4b"), "identical-bytes")

	store := newFakeCollisionStore()
	store.add(&database.BookFile{ID: "f1", BookID: "b1", FilePath: src, FileSize: 15})
	store.add(&database.BookFile{ID: "f2", BookID: "b1", FilePath: dst, FileSize: 15})

	if _, err := RenameFiles(
		[]FileRenameEntry{{SegmentID: "f1", SourcePath: src, TargetPath: dst, ExpectedSize: 15}},
		&CollisionPolicy{RootDir: root, BookID: "b1", Store: store},
	); err != nil {
		t.Fatalf("RenameFiles: %v", err)
	}

	loser, _ := store.GetBookFileByID("b1", "f1")
	if loser == nil {
		t.Fatal("the losing row was DELETED; it must only be tombstoned")
	}
	if !loser.Missing {
		t.Fatalf("expected the losing row to be tombstoned (Missing=true), got %+v", loser)
	}
	survivor, _ := store.GetBookFileByID("b1", "f2")
	if survivor == nil || survivor.FilePath != dst {
		t.Fatalf("the surviving row must be left pointing at the kept file, got %+v", survivor)
	}
}

// TestRenameFiles_InLibraryDifferent_UsesCopyNLadder is the case the plan flags
// as probably the most common: same computed target, different bytes. Apply
// must resolve it the way organize does, with _copyN.
func TestRenameFiles_InLibraryDifferent_UsesCopyNLadder(t *testing.T) {
	root := t.TempDir()
	src := writeCollisionFile(t, filepath.Join(root, "incoming", "book.m4b"), "ours-aaaa")
	dst := writeCollisionFile(t, filepath.Join(root, "Author", "Title.m4b"), "theirs-bb")

	store := newFakeCollisionStore()
	store.add(&database.BookFile{ID: "f1", BookID: "b1", FilePath: src})

	result, err := RenameFiles(
		[]FileRenameEntry{{SegmentID: "f1", SourcePath: src, TargetPath: dst}},
		&CollisionPolicy{RootDir: root, BookID: "b1", Store: store},
	)
	if err != nil {
		t.Fatalf("RenameFiles: %v", err)
	}
	want := filepath.Join(root, "Author", "Title_copy1.m4b")
	if len(result.Succeeded) != 1 || result.Succeeded[0].TargetPath != want {
		t.Fatalf("expected the rename to land at %s, got %+v", want, result.Succeeded)
	}
	if got := readCollisionFile(t, dst); got != "theirs-bb" {
		t.Fatalf("the occupant was overwritten: %q", got)
	}
	if got := readCollisionFile(t, want); got != "ours-aaaa" {
		t.Fatalf("our bytes did not land at the _copyN path: %q", got)
	}
	if len(result.Resolutions) != 1 || result.Resolutions[0].Action != CollisionRetargeted {
		t.Fatalf("expected one retargeted resolution, got %+v", result.Resolutions)
	}
}

// TestRenameFiles_OutsideLibraryOccupant covers both outcomes of the
// out-of-tree branch, where identity is the size + re-stat PROXY rather than a
// hash. The occupant is a symlink out of the library tree — what the `symlink`
// organization strategy leaves behind.
func TestRenameFiles_OutsideLibraryOccupant(t *testing.T) {
	for _, tc := range []struct {
		name          string
		outsideBytes  string
		wantAction    CollisionAction
		wantSucceeded int
	}{
		{name: "passes the proxy: already linked, nothing moves", outsideBytes: "same-size", wantAction: CollisionAlreadyLinked},
		{name: "fails the proxy: falls back to the _copyN ladder", outsideBytes: "different-length-bytes", wantAction: CollisionRetargeted, wantSucceeded: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "library")
			outside := filepath.Join(base, "outside")
			src := writeCollisionFile(t, filepath.Join(root, "incoming", "book.m4b"), "same-size")
			real := writeCollisionFile(t, filepath.Join(outside, "real.m4b"), tc.outsideBytes)

			dst := filepath.Join(root, "Author", "Title.m4b")
			if err := os.MkdirAll(filepath.Dir(dst), 0o775); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(real, dst); err != nil {
				t.Skipf("filesystem has no symlinks: %v", err)
			}

			store := newFakeCollisionStore()
			store.add(&database.BookFile{ID: "f1", BookID: "b1", FilePath: src})

			result, err := RenameFiles(
				[]FileRenameEntry{{SegmentID: "f1", SourcePath: src, TargetPath: dst}},
				&CollisionPolicy{RootDir: root, BookID: "b1", Store: store},
			)
			if err != nil {
				t.Fatalf("RenameFiles: %v", err)
			}
			if len(result.Resolutions) != 1 || result.Resolutions[0].Action != tc.wantAction {
				t.Fatalf("want action %s, got %+v", tc.wantAction, result.Resolutions)
			}
			if len(result.Succeeded) != tc.wantSucceeded {
				t.Fatalf("want %d succeeded renames, got %d", tc.wantSucceeded, len(result.Succeeded))
			}
			// The out-of-tree file is never touched, in either outcome.
			if got := readCollisionFile(t, real); got != tc.outsideBytes {
				t.Fatalf("the out-of-tree file was modified: %q", got)
			}
			if tc.wantAction == CollisionAlreadyLinked {
				// Nothing moved: our source is still where it was, and no row
				// was rewritten.
				mustCollisionExist(t, src)
				if store.updates != 0 {
					t.Fatalf("already-linked must not rewrite any row, got %d updates", store.updates)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// rollback safety — the most important test
// ---------------------------------------------------------------------------

// TestRenameFiles_RollsBackResolvedCollisionWhenALaterFileFails is the
// guarantee the whole ordering argument rests on.
//
// A two-file book: file 1's collision is resolved in the pre-flight pass
// (source quarantined, row repointed); file 2 then fails in phase 1. Nothing
// may be lost and nothing may be left stranded — file 1's source is back where
// it was, the occupant it collided with is untouched, and the row points where
// it pointed before.
func TestRenameFiles_RollsBackResolvedCollisionWhenALaterFileFails(t *testing.T) {
	root := t.TempDir()

	// File 1: collides with a byte-identical occupant.
	src1 := writeCollisionFile(t, filepath.Join(root, "incoming", "one.m4b"), "identical-bytes")
	dst1 := writeCollisionFile(t, filepath.Join(root, "Author", "Title - 01.m4b"), "identical-bytes")

	// File 2: made to fail in phase 1. Its target's PARENT is a regular file,
	// so MkdirAll returns ENOTDIR before anything is parked.
	src2 := writeCollisionFile(t, filepath.Join(root, "incoming", "two.m4b"), "second-file")
	blocker := writeCollisionFile(t, filepath.Join(root, "Author", "blocker"), "not a directory")
	dst2 := filepath.Join(blocker, "Title - 02.m4b")

	store := newFakeCollisionStore()
	store.add(&database.BookFile{ID: "f1", BookID: "b1", FilePath: src1, FileSize: 15})
	store.add(&database.BookFile{ID: "f2", BookID: "b1", FilePath: src2})

	result, err := RenameFiles(
		[]FileRenameEntry{
			{SegmentID: "f1", SourcePath: src1, TargetPath: dst1, ExpectedSize: 15},
			{SegmentID: "f2", SourcePath: src2, TargetPath: dst2},
		},
		&CollisionPolicy{RootDir: root, BookID: "b1", Store: store},
	)
	if err == nil {
		t.Fatal("expected the batch to fail on file 2")
	}
	if len(result.Errors) != 0 {
		t.Fatalf("rollback reported failures of its own: %v", result.Errors)
	}

	// Nothing is lost: the occupant still has its bytes...
	if got := readCollisionFile(t, dst1); got != "identical-bytes" {
		t.Fatalf("occupant lost or changed: %q", got)
	}
	// ...our file 1 is back at its source, not stranded in quarantine...
	if got := readCollisionFile(t, src1); got != "identical-bytes" {
		t.Fatalf("file 1 was not restored to its source path: %q", got)
	}
	// ...file 2 never moved...
	if got := readCollisionFile(t, src2); got != "second-file" {
		t.Fatalf("file 2 moved despite the failure: %q", got)
	}
	// ...the quarantine tree is empty of this book's file...
	qDir := filepath.Join(root, ".failed", "_collisions")
	if entries, derr := os.ReadDir(qDir); derr == nil {
		for _, e := range entries {
			inner, _ := os.ReadDir(filepath.Join(qDir, e.Name()))
			if len(inner) > 0 {
				t.Fatalf("file left stranded in quarantine: %s/%s", e.Name(), inner[0].Name())
			}
		}
	}
	// ...and the row points where it did before the pass ran.
	row, _ := store.GetBookFileByID("b1", "f1")
	if row == nil || row.FilePath != src1 || row.Missing {
		t.Fatalf("row was not restored: %+v", row)
	}
	// Sanity: the blocker file that caused the failure is untouched.
	if got := readCollisionFile(t, blocker); got != "not a directory" {
		t.Fatalf("blocker changed: %q", got)
	}
}

// TestRenameFiles_UnresolvableCollisionReportsOccupantFingerprint proves an
// unresolvable collision comes back as a *CollisionError carrying the occupant
// size and mtime the durable-failure record needs to self-heal, and that
// nothing was mutated on the way out.
func TestRenameFiles_UnresolvableCollisionReportsOccupantFingerprint(t *testing.T) {
	root := t.TempDir()
	src := writeCollisionFile(t, filepath.Join(root, "incoming", "book.m4b"), "identical-bytes")
	dst := writeCollisionFile(t, filepath.Join(root, "Author", "Title.m4b"), "identical-bytes")

	store := newFakeCollisionStore()
	store.add(&database.BookFile{ID: "f1", BookID: "b1", FilePath: src})
	store.failUpdate = true // the repoint cannot be persisted

	result, err := RenameFiles(
		[]FileRenameEntry{{SegmentID: "f1", SourcePath: src, TargetPath: dst}},
		&CollisionPolicy{RootDir: root, BookID: "b1", Store: store},
	)
	var cerr *CollisionError
	if !errors.As(err, &cerr) {
		t.Fatalf("expected a *CollisionError, got %v", err)
	}
	if len(cerr.Failures) != 1 {
		t.Fatalf("expected one failure, got %+v", cerr.Failures)
	}
	f := cerr.Failures[0]
	if f.OccupantSize != int64(len("identical-bytes")) || f.OccupantModUnix == 0 {
		t.Fatalf("failure is missing the occupant fingerprint the self-heal needs: %+v", f)
	}
	if len(result.Collisions) != 1 {
		t.Fatalf("the failure must also surface on the result: %+v", result.Collisions)
	}
	// The quarantine move is rolled back even though the row write failed.
	if got := readCollisionFile(t, src); got != "identical-bytes" {
		t.Fatalf("source not restored after a failed repoint: %q", got)
	}
	if got := readCollisionFile(t, dst); got != "identical-bytes" {
		t.Fatalf("occupant changed: %q", got)
	}
}

// TestRenameFiles_KeepsResolvedCollisionAfterAPartialPublish covers the OTHER
// side of the rollback rule — the branch the ordering comment singles out and
// the sibling test above does not reach.
//
// The sibling fails in phase 1, before anything is published, so it takes the
// unconditional journal.rollback path. Here file 2 PUBLISHES and file 3 then
// fails in phase 2, so len(result.Succeeded) == 1 and the journal is
// deliberately KEPT: undoing file 1's quarantine would move a file back to a
// source path whose row now points at a published target.
//
// The phase-2 failure is produced without any injection hook, in a shape the
// planner really emits: files 2 and 3 format to the SAME target name. At
// pre-flight time that target is empty, so the resolver correctly ignores both
// (intra-book collisions are planPass's job, via forceTrackSuffix) — they park
// on distinct nonced temps, file 2 publishes, and file 3's finalizeExclusive
// hits EEXIST against what file 2 just wrote.
func TestRenameFiles_KeepsResolvedCollisionAfterAPartialPublish(t *testing.T) {
	root := t.TempDir()

	// File 1: collides with a byte-identical occupant -> quarantine + repoint.
	src1 := writeCollisionFile(t, filepath.Join(root, "incoming", "one.m4b"), "identical-bytes")
	dst1 := writeCollisionFile(t, filepath.Join(root, "Author", "Title - 01.m4b"), "identical-bytes")

	// Files 2 and 3 both target the same free path.
	shared := filepath.Join(root, "Author", "Title - 02.m4b")
	src2 := writeCollisionFile(t, filepath.Join(root, "incoming", "two.m4b"), "second-file")
	src3 := writeCollisionFile(t, filepath.Join(root, "incoming", "three.m4b"), "third-file")

	store := newFakeCollisionStore()
	store.add(&database.BookFile{ID: "f1", BookID: "b1", FilePath: src1})
	store.add(&database.BookFile{ID: "f2", BookID: "b1", FilePath: src2})
	store.add(&database.BookFile{ID: "f3", BookID: "b1", FilePath: src3})

	result, err := RenameFiles(
		[]FileRenameEntry{
			{SegmentID: "f1", SourcePath: src1, TargetPath: dst1},
			{SegmentID: "f2", SourcePath: src2, TargetPath: shared},
			{SegmentID: "f3", SourcePath: src3, TargetPath: shared},
		},
		&CollisionPolicy{RootDir: root, BookID: "b1", Store: store},
	)
	if err == nil {
		t.Fatal("expected file 3 to fail publishing into file 2's target")
	}
	// The kept-journal path is not itself an error condition: nothing failed to
	// roll back, because nothing was rolled back.
	if len(result.Errors) != 0 {
		t.Fatalf("unexpected rollback errors: %v", result.Errors)
	}
	if len(result.Succeeded) != 1 || result.Succeeded[0].SegmentID != "f2" {
		t.Fatalf("expected exactly file 2 to be published, got %+v", result.Succeeded)
	}

	// File 1's resolution STANDS: source quarantined, occupant intact, row
	// repointed at the copy that was kept.
	if _, statErr := os.Lstat(src1); !os.IsNotExist(statErr) {
		t.Fatalf("file 1's source came back despite the partial publish (err=%v)", statErr)
	}
	if got := readCollisionFile(t, dst1); got != "identical-bytes" {
		t.Fatalf("occupant lost or changed: %q", got)
	}
	if len(result.Resolutions) != 1 || result.Resolutions[0].Action != CollisionQuarantinedSource {
		t.Fatalf("expected the resolution to be reported and kept, got %+v", result.Resolutions)
	}
	if q := result.Resolutions[0].QuarantinePath; readCollisionFile(t, q) != "identical-bytes" {
		t.Fatalf("quarantined file missing or changed at %s", q)
	}
	if row, _ := store.GetBookFileByID("b1", "f1"); row == nil || row.FilePath != dst1 {
		t.Fatalf("row was not left repointed at the kept copy: %+v", row)
	}

	// File 2 stays published where it landed.
	if got := readCollisionFile(t, shared); got != "second-file" {
		t.Fatalf("published file 2 was disturbed: %q", got)
	}
	// File 3 is returned to its source — never left at a .tmp-rename path.
	if got := readCollisionFile(t, src3); got != "third-file" {
		t.Fatalf("file 3 was not rolled back to its source: %q", got)
	}
	strays, _ := filepath.Glob(filepath.Join(root, "Author", "*.tmp-rename-*"))
	if len(strays) != 0 {
		t.Fatalf("temp files left stranded: %v", strays)
	}
}

// TestOccupantOutsideRoot pins the library-boundary predicate, and in
// particular its ERROR contract — the one thing about it that is not obvious.
//
// It deliberately does NOT swallow every resolution failure into "in-library".
// Only NOT-EXIST means in-library: a broken link really is not evidence of an
// outside file. Any other failure is returned, because os.Stat FOLLOWS the link
// and would hand the caller the outside file's bytes while the caller believed
// it was comparing an in-library pair — and a match there would quarantine our
// source and repoint the row at a symlink leaving the tree. An undecidable
// boundary must become an unresolvable collision, not a guess.
func TestOccupantOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outsideDir := t.TempDir()

	inside := writeCollisionFile(t, filepath.Join(root, "Author", "inside.m4b"), "x")

	// A symlink out of the tree — what the `symlink` organization strategy
	// leaves behind, and the only way an occupant AT a target under RootDir can
	// actually live outside it.
	outsideReal := writeCollisionFile(t, filepath.Join(outsideDir, "real.m4b"), "x")
	outsideLink := filepath.Join(root, "Author", "linked.m4b")
	if err := os.Symlink(outsideReal, outsideLink); err != nil {
		t.Skipf("filesystem has no symlinks: %v", err)
	}

	dangling := filepath.Join(root, "Author", "dangling.m4b")
	if err := os.Symlink(filepath.Join(outsideDir, "gone.m4b"), dangling); err != nil {
		t.Skipf("filesystem has no symlinks: %v", err)
	}

	t.Run("a real file under the root is in-library", func(t *testing.T) {
		got, err := occupantOutsideRoot(inside, root)
		if err != nil || got {
			t.Fatalf("occupantOutsideRoot = (%v, %v), want (false, nil)", got, err)
		}
	})

	t.Run("a symlink out of the tree is outside", func(t *testing.T) {
		got, err := occupantOutsideRoot(outsideLink, root)
		if err != nil || !got {
			t.Fatalf("occupantOutsideRoot = (%v, %v), want (true, nil)", got, err)
		}
	})

	t.Run("a dangling link is in-library, not an error", func(t *testing.T) {
		got, err := occupantOutsideRoot(dangling, root)
		if err != nil || got {
			t.Fatalf("occupantOutsideRoot = (%v, %v), want (false, nil)", got, err)
		}
	})

	t.Run("an empty root disables the boundary test", func(t *testing.T) {
		got, err := occupantOutsideRoot(outsideLink, "")
		if err != nil || got {
			t.Fatalf("occupantOutsideRoot = (%v, %v), want (false, nil)", got, err)
		}
	})

	// The case the narrowing exists for: an unresolvable link that is NOT
	// simply absent must surface as an error rather than defaulting to
	// in-library.
	t.Run("an unresolvable link is an error, not a silent in-library default", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root traverses regardless of mode bits")
		}
		locked := filepath.Join(outsideDir, "locked")
		if err := os.MkdirAll(locked, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		hidden := writeCollisionFile(t, filepath.Join(locked, "real.m4b"), "x")
		link := filepath.Join(root, "Author", "locked.m4b")
		if err := os.Symlink(hidden, link); err != nil {
			t.Skipf("filesystem has no symlinks: %v", err)
		}
		if err := os.Chmod(locked, 0o000); err != nil {
			t.Skipf("cannot drop directory permissions: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

		if _, err := os.Lstat(filepath.Join(locked, "real.m4b")); err == nil {
			t.Skip("permissions are not enforced on this filesystem")
		}
		got, err := occupantOutsideRoot(link, root)
		if err == nil {
			t.Fatalf("an unresolvable occupant must be an error, got (%v, nil)", got)
		}
		if got {
			t.Fatalf("an errored boundary test must not also claim 'outside': %v", got)
		}
	})
}

// TestRenameFiles_NilPolicyKeepsTheOldRefusal pins the explicit opt-out: with
// no policy an occupied target still fails the batch and rolls back, exactly as
// it did before the resolver existed.
func TestRenameFiles_NilPolicyKeepsTheOldRefusal(t *testing.T) {
	root := t.TempDir()
	src := writeCollisionFile(t, filepath.Join(root, "incoming", "book.m4b"), "ours")
	dst := writeCollisionFile(t, filepath.Join(root, "Author", "Title.m4b"), "theirs")

	_, err := RenameFiles([]FileRenameEntry{{SegmentID: "f1", SourcePath: src, TargetPath: dst}}, nil)
	if err == nil {
		t.Fatal("expected the occupied target to fail the batch with no policy")
	}
	if got := readCollisionFile(t, dst); got != "theirs" {
		t.Fatalf("occupant destroyed: %q", got)
	}
	if got := readCollisionFile(t, src); got != "ours" {
		t.Fatalf("source not rolled back: %q", got)
	}
}

// TestQuarantineCollisionSource_DisambiguatesAndStaysInsideFailed pins the two
// properties a later sweep depends on: the file lands under the existing
// `.failed` tree (already excluded everywhere), and a second loser with the
// same basename does not clobber the first.
func TestQuarantineCollisionSource_DisambiguatesAndStaysInsideFailed(t *testing.T) {
	root := t.TempDir()
	a := writeCollisionFile(t, filepath.Join(root, "a", "book.m4b"), "first")
	b := writeCollisionFile(t, filepath.Join(root, "b", "book.m4b"), "second")

	p1, err := quarantineCollisionSource(root, "b1", a)
	if err != nil {
		t.Fatalf("quarantine 1: %v", err)
	}
	p2, err := quarantineCollisionSource(root, "b1", b)
	if err != nil {
		t.Fatalf("quarantine 2: %v", err)
	}
	if p1 == p2 {
		t.Fatal("second loser clobbered the first")
	}
	for _, p := range []string{p1, p2} {
		if !strings.HasPrefix(p, filepath.Join(root, ".failed", "_collisions")) {
			t.Fatalf("%s escaped the collision quarantine tree", p)
		}
	}
	if readCollisionFile(t, p1) != "first" || readCollisionFile(t, p2) != "second" {
		t.Fatal("quarantined bytes were swapped or truncated")
	}
	mustCollisionNotExist(t, a)
	mustCollisionNotExist(t, b)
}

// ---------------------------------------------------------------------------
// durable failure + self-heal
// ---------------------------------------------------------------------------

// TestApplyRenameBlocked_SkipsThenSelfHeals is the "don't let one bad item
// constantly make it fail" requirement: an unresolvable collision must be
// skipped on later runs, and must come back on its own when the block clears.
func TestApplyRenameBlocked_SkipsThenSelfHeals(t *testing.T) {
	dir := t.TempDir()
	occupant := writeCollisionFile(t, filepath.Join(dir, "Title.m4b"), "occupant")
	info, err := os.Lstat(occupant)
	if err != nil {
		t.Fatal(err)
	}

	newRecorded := func() *fakePrefStore {
		prefs := newFakePrefStore()
		RecordApplyRenameFailure(prefs, ApplyRenameFailure{
			BookID:          "b1",
			TargetPath:      occupant,
			OccupantPath:    occupant,
			OccupantSize:    info.Size(),
			OccupantModUnix: info.ModTime().Unix(),
			Reason:          "occupant is byte-identical but the repoint failed",
		})
		return prefs
	}

	t.Run("blocked while nothing has changed", func(t *testing.T) {
		prefs := newRecorded()
		if !ApplyRenameBlocked(prefs, "b1", []string{occupant}) {
			t.Fatal("expected the book to be skipped on the next run")
		}
		// Still blocked on a repeat run — the record is durable, not one-shot.
		if !ApplyRenameBlocked(prefs, "b1", []string{occupant}) {
			t.Fatal("the record must survive being read")
		}
	})

	t.Run("self-heals when the blocking file goes away", func(t *testing.T) {
		prefs := newRecorded()
		gone := filepath.Join(dir, "gone.m4b")
		writeCollisionFile(t, gone, "occupant")
		gi, _ := os.Lstat(gone)
		RecordApplyRenameFailure(prefs, ApplyRenameFailure{
			BookID: "b2", TargetPath: gone, OccupantPath: gone,
			OccupantSize: gi.Size(), OccupantModUnix: gi.ModTime().Unix(),
		})
		if !ApplyRenameBlocked(prefs, "b2", []string{gone}) {
			t.Fatal("expected b2 to start out blocked")
		}
		if err := os.Remove(gone); err != nil {
			t.Fatal(err)
		}
		if ApplyRenameBlocked(prefs, "b2", []string{gone}) {
			t.Fatal("the block must clear once the occupant is gone")
		}
		if _, ok := LoadApplyRenameFailure(prefs, "b2"); ok {
			t.Fatal("the record must be cleared, not merely ignored")
		}
	})

	t.Run("self-heals when the blocking file changes", func(t *testing.T) {
		prefs := newRecorded()
		if err := os.WriteFile(occupant, []byte("replaced entirely"), 0o644); err != nil {
			t.Fatal(err)
		}
		if ApplyRenameBlocked(prefs, "b1", []string{occupant}) {
			t.Fatal("the block must clear once the occupant changed")
		}
	})

	t.Run("self-heals when the book now targets somewhere else", func(t *testing.T) {
		prefs := newRecorded()
		if ApplyRenameBlocked(prefs, "b1", []string{filepath.Join(dir, "A Brand New Title.m4b")}) {
			t.Fatal("a book that no longer targets the blocked path must not be skipped")
		}
	})

	t.Run("a cleared record reads as absent, not as a decode error", func(t *testing.T) {
		prefs := newRecorded()
		ClearApplyRenameFailure(prefs, "b1")
		if _, ok := LoadApplyRenameFailure(prefs, "b1"); ok {
			t.Fatal("a blanked value must read as no record")
		}
		if ApplyRenameBlocked(prefs, "b1", []string{occupant}) {
			t.Fatal("a cleared record must not block")
		}
	})

	t.Run("an unreadable record never blocks forever", func(t *testing.T) {
		prefs := newFakePrefStore()
		_ = prefs.SetUserPreferenceForUser("_system", ApplyRenameFailurePrefix+"b9", "{not json")
		if ApplyRenameBlocked(prefs, "b9", nil) {
			t.Fatal("a record that cannot be decoded must not block the book")
		}
	})
}
