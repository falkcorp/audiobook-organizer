// file: internal/plugins/maintenance/fragment_copy_content.go
// version: 1.0.0
// guid: 4d7b2e95-1c6a-4f38-8e0d-b5a9c3f1e762
// last-edited: 2026-10-06

// Content proof of a copy claimant (owner decision 2026-10-06, "hash both,
// read-only"). A fragment that matches a parent row by its original name and
// size alone is an unproven copy. When both files are on disk at the same
// size, the plan reads and hashes both (filehash.BookFileHash, the digest
// book_files.file_hash holds) and equal digests prove the copy: the pair's
// evidence becomes fragEvContentHashPrefix plus the digest and the row lands
// in copy:<parent>, not copy-unproven:<parent>.
//
// READ-ONLY. Nothing is stored on either book or row: the proof lives in the
// plan row (its evidence, its fingerprint, and fragParentState.ContentProofs,
// which carries each file's size and mtime as read). A re-plan, including the
// one Apply runs under the merge lock, never re-hashes: it re-stats both
// files and refuses the row (changed since plan) when either size or mtime
// moved (restoreContentProofs).
//
// Never read: a file under the iTunes library (either side), and a file above
// filehash.Threshold, which BookFileHash samples (head, tail and
// size) rather than reads whole: equal digests there would not prove
// byte-identical content, so such a pair stays unproven and says why. A
// hands-off claimant outside the iTunes library (an iTunes id, Doctor Who /
// Big Finish) IS compared (owner decision 2026-10-06, "list; I apply them"):
// its row stays manual-only and never applicable, with the proof in its
// evidence for the owner to act on by hand.
package maintenance

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/falkcorp/audiobook-organizer/internal/filehash"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// Content evidence. fragEvContentHashPrefix opens a proven pair's evidence
// (the digest follows); the two suffixes are appended to a name-and-size
// evidence whose content was compared and differs, or could not be.
const (
	fragEvContentHashPrefix  = "content hash equal at plan time: sha256:"
	fragEvContentDiffers     = "; content differs: "
	fragEvContentNotCompared = "; content not compared: "
)

// fragHashLimit bounds the hashing pool: the work is file reads off the
// library's NAS, not CPU, so a small fixed limit (CLAUDE.md, concurrency).
const fragHashLimit = 4

// fragFileSig is a file's size and modification time as read: a proof holds
// only while both are unchanged.
type fragFileSig struct {
	Size    int64 `json:"size"`
	MtimeNS int64 `json:"mtime_ns"`
}

func sigOf(fi os.FileInfo) fragFileSig {
	return fragFileSig{Size: fi.Size(), MtimeNS: fi.ModTime().UnixNano()}
}

func (s fragFileSig) String() string {
	return fmt.Sprintf("%d bytes, mtime %d", s.Size, s.MtimeNS)
}

// fragHashFile is the default fragmentFixer.hashFn: filehash.BookFileHash of
// the file at path, streamed (no decoding), with the signature of the bytes
// it read. The descriptor is stat'ed before and after the read; a file that
// changed meanwhile is an error, never a digest.
func fragHashFile(path string) (fragFileSig, string, error) {
	fh, err := os.Open(path)
	if err != nil {
		return fragFileSig{}, "", err
	}
	defer fh.Close()
	before, err := fh.Stat()
	if err != nil {
		return fragFileSig{}, "", err
	}
	sum, err := filehash.BookFileHashFromFile(fh, before.Size())
	if err != nil {
		return fragFileSig{}, "", err
	}
	after, err := fh.Stat()
	if err != nil {
		return fragFileSig{}, "", err
	}
	if sigOf(before) != sigOf(after) {
		return fragFileSig{}, "", fmt.Errorf("%s changed while it was read (%s, then %s)", path, sigOf(before), sigOf(after))
	}
	return sigOf(before), sum, nil
}

// fragContentProof is the content comparison of one fragment file against
// one parent row's file. Only equal proofs are stored with the plan
// (fragParentState.ContentProofs); NotCompared is plan-time only.
type fragContentProof struct {
	FragBook     string      `json:"frag_book"`
	FragFile     string      `json:"frag_file"`
	FragPath     string      `json:"frag_path"`
	FragSig      fragFileSig `json:"frag_sig"`
	FragDigest   string      `json:"frag_digest"`
	ParentRow    string      `json:"parent_row"`
	ParentPath   string      `json:"parent_path"`
	ParentSig    fragFileSig `json:"parent_sig"`
	ParentDigest string      `json:"parent_digest"`
	NotCompared  string      `json:"-"`
}

func contentKey(fragFileID, parentRowID string) string { return fragFileID + "|" + parentRowID }

func (p fragContentProof) equal() bool {
	return p.NotCompared == "" && p.FragDigest != "" && p.FragDigest == p.ParentDigest
}

// fingerprint is the proof as the row's fingerprint carries it: a re-plan
// rebuilds it from the stored proof only while both files are unchanged.
func (p fragContentProof) fingerprint() string {
	return strings.Join([]string{"content", p.FragFile, p.FragPath, p.FragSig.String(), p.ParentRow, p.ParentPath,
		p.ParentSig.String(), p.FragDigest}, "|")
}

// withContent rewrites the name-and-size evidence of c's matches that the
// plan compared by content: proven (equal digests), differing, or not
// compared (the reason follows).
func (lib *fragLibrary) withContent(c *fragCandidate, ms []fragMatch) []fragMatch {
	if len(lib.content) == 0 {
		return ms
	}
	out, copied := ms, false
	for i, m := range ms {
		if m.Evidence != fragEvNameSize && m.Evidence != fragEvNameSizeFolder {
			continue
		}
		p, ok := lib.content[contentKey(c.File.ID, m.Row.ID)]
		if !ok || p.FragPath != c.File.Path || p.ParentPath != m.Row.Path {
			continue
		}
		if !copied {
			out, copied = append([]fragMatch(nil), ms...), true
		}
		switch {
		case p.NotCompared != "":
			out[i].Evidence = m.Evidence + fragEvContentNotCompared + p.NotCompared
		case p.equal():
			out[i].Evidence = fragEvContentHashPrefix + p.FragDigest
		default:
			out[i].Evidence = fmt.Sprintf("%s%ssha256:%s vs the parent row's sha256:%s", m.Evidence, fragEvContentDiffers, p.FragDigest, p.ParentDigest)
		}
	}
	return out
}

// contentProofOf is the plan's proof behind pair p's evidence, ok only for a
// pair the content proved (a path twin's proof is its donor's).
func (lib *fragLibrary) contentProofOf(p fragPair) (fragContentProof, bool) {
	if !strings.HasPrefix(p.Evidence, fragEvContentHashPrefix) {
		return fragContentProof{}, false
	}
	pr, ok := lib.content[contentKey(p.Frag.File.ID, p.Parent.ID)]
	return pr, ok && pr.equal()
}

// proveCopiesByContent hashes, on a bounded pool, each copy claimant that
// would otherwise be unproven: a present fragment whose one match (after the
// iTunes-parent rule) is by original name and size, to a parent row whose
// file is on disk at the fragment's size, neither side hands-off. Results go
// to lib.content. A file read error is a NotCompared proof (the pair stays
// unproven and says why); only cancellation fails the plan.
func (f *fragmentFixer) proveCopiesByContent(ctx context.Context, rep registry.Reporter, lib *fragLibrary, ix *fragIndex, cands []*fragCandidate) error {
	type job struct {
		c *fragCandidate
		m fragMatch
	}
	var jobs []job
	for _, c := range cands {
		if !c.Present || c.StatErr != "" || c.DiskSize <= 0 {
			continue
		}
		ms, _, _, _ := f.effectiveMatches(lib, ix, c)
		if len(ms) != 1 || (ms[0].Evidence != fragEvNameSize && ms[0].Evidence != fragEvNameSizeFolder) {
			continue
		}
		// A file under the iTunes library is never read. A hands-off
		// claimant elsewhere (an iTunes id, Doctor Who / Big Finish) is read:
		// its row is manual-only whatever the content, and the proof is shown
		// on it for the owner who applies it by hand.
		if !lib.readableForProof(c.Book.ID, c.File.Path) || !lib.readableForProof(ms[0].Row.BookID, ms[0].Row.Path) {
			continue
		}
		jobs = append(jobs, job{c: c, m: ms[0]})
	}
	if len(jobs) == 0 {
		return nil
	}
	// One read per file: several claimants share one parent row's file.
	type hashed struct {
		sig    fragFileSig
		digest string
		err    error
	}
	var memoMu sync.Mutex
	memo := map[string]func() hashed{}
	hashOf := func(path string) hashed {
		memoMu.Lock()
		fn, ok := memo[path]
		if !ok {
			fn = sync.OnceValue(func() hashed {
				s, d, err := f.hashFn(path)
				return hashed{s, d, err}
			})
			memo[path] = fn
		}
		memoMu.Unlock()
		return fn()
	}
	out := make([]*fragContentProof, len(jobs))
	var done atomic.Int64
	// Each worker writes only out[i].
	err := registry.RunItems(ctx, rep, fbIndexes(len(jobs)), func(ctx context.Context, i int) error {
		defer done.Add(1)
		if err := ctx.Err(); err != nil {
			return err
		}
		j := jobs[i]
		fi, err := f.statFn(j.m.Row.Path)
		if err != nil || fi.Size() != j.c.DiskSize {
			return nil // not a same-size copy on disk: pairFor decides it
		}
		p := &fragContentProof{FragBook: j.c.Book.ID, FragFile: j.c.File.ID, FragPath: j.c.File.Path,
			ParentRow: j.m.Row.ID, ParentPath: j.m.Row.Path}
		out[i] = p
		if j.c.DiskSize > filehash.Threshold {
			p.NotCompared = fmt.Sprintf("the files are over %d MB (filehash.Threshold), where the file hash samples the head and tail rather than every byte, so equal hashes would not prove identical content",
				filehash.Threshold/(1024*1024))
			return nil
		}
		fr := hashOf(j.c.File.Path)
		if err := ctx.Err(); err != nil {
			return err
		}
		if fr.err != nil {
			p.NotCompared = "the fragment's file is unreadable: " + fr.err.Error()
			return nil
		}
		pr := hashOf(j.m.Row.Path)
		if err := ctx.Err(); err != nil {
			return err
		}
		if pr.err != nil {
			p.NotCompared = "the parent row's file is unreadable: " + pr.err.Error()
			return nil
		}
		p.FragSig, p.FragDigest, p.ParentSig, p.ParentDigest = fr.sig, fr.digest, pr.sig, pr.digest
		return nil
	}, registry.RunItemsOptions{
		Concurrency: fragHashLimit,
		ErrMode:     registry.ErrModeFail,
		Label:       func(_, total int) string { return fmt.Sprintf("Copy content %d/%d", done.Load(), total) },
	})
	if err != nil {
		return fmt.Errorf("%s: copy content: %w", fragFixerID, err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%s: copy content: %w", fragFixerID, err)
	}
	for _, p := range out {
		if p != nil {
			lib.content[contentKey(p.FragFile, p.ParentRow)] = *p
		}
	}
	return nil
}

// readableForProof reports whether the proof may read the file at path of
// book id: never one under the iTunes library (the guard's iTunes path rule,
// symlinks resolved), nor one the guard cannot resolve.
func (lib *fragLibrary) readableForProof(id, path string) bool {
	k, _ := repairs.GuardBookPathsWith(lib.paths, id, []string{path}, "")
	return k != repairs.SkipITunes && k != repairs.SkipGuardUnreadable
}

// restoreContentProofs puts the plan's content proofs back into a re-plan's
// snapshot after re-stating both files of each: no file is re-read (a re-plan
// runs under the merge lock). A file whose size or mtime moved, or that is
// gone, is a change ("" when every proof still holds).
func (f *fragmentFixer) restoreContentProofs(lib *fragLibrary, proofs []fragContentProof) string {
	sort.Slice(proofs, func(i, j int) bool { return proofs[i].FragFile < proofs[j].FragFile })
	for _, p := range proofs {
		for _, side := range []struct {
			path string
			sig  fragFileSig
		}{{p.FragPath, p.FragSig}, {p.ParentPath, p.ParentSig}} {
			fi, err := f.statFn(side.path)
			if err != nil {
				return fmt.Sprintf("file %s, whose content proved fragment %s a copy, cannot be read now: %v", side.path, p.FragBook, err)
			}
			if now := sigOf(fi); now != side.sig {
				return fmt.Sprintf("file %s changed since the plan compared its content (%s then, %s now)", side.path, side.sig, now)
			}
		}
		if p.FragDigest == "" || p.FragDigest != p.ParentDigest {
			return fmt.Sprintf("the plan's content proof for fragment %s is not an equal pair", p.FragBook)
		}
		lib.content[contentKey(p.FragFile, p.ParentRow)] = p
	}
	return ""
}
