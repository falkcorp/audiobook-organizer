// file: internal/plugins/maintenance/extract_wav_clips.go
// version: 1.7.0
// guid: e1f2a3b4-c5d6-7890-abcd-ef1234567890
// last-edited: 2026-09-01

package maintenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/filehash"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

const extractWAVPageSize = 500 // larger pages — pure I/O, no GPU wait

func (p *Plugin) extractWAVClipsDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "maintenance.extract-wav-clips",
		Liveness:        sdk.LivenessRunItems,
		Plugin:          "maintenance",
		DisplayName:     "Extract WAV clips for transcription cache",
		Description:     "Extracts the first 90 seconds of each book's first audio file and saves the result in {library}/.wav-cache/{hash}.wav. Also hashes the source file with the canonical book_files.file_hash digest (internal/filehash), persists it to BookFile.FileHash only when that row has none, and creates a content-stable hardlink so the transcription cache survives organize path changes.",
		ResumePolicy:    sdk.ResumeRestart,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.extract-wav-clips",
		Cancellable:     true,
		Timeout:         24 * time.Hour,
		ProgressTimeout: 10 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapFilesRead, sdk.CapFilesWrite},
		Run:             p.runExtractWAVClips,
	}
}

type extractWAVParams struct {
	SkipExisting *bool `json:"skip_existing,omitempty"` // default true
}

func (p *Plugin) runExtractWAVClips(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}

	var params extractWAVParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("maintenance.extract-wav-clips: decode params: %w", err)
		}
	}
	skipExisting := params.SkipExisting == nil || *params.SkipExisting

	log := reporter.Logger()
	rootDir := p.deps.RootDir()
	cacheDir := wavCacheDir(rootDir)

	if err := os.MkdirAll(cacheDir, 0o775); err != nil {
		return fmt.Errorf("create wav cache dir %s: %w", cacheDir, err)
	}

	allIDs, err := store.ListBookIDs()
	if err != nil {
		return fmt.Errorf("list book ids: %w", err)
	}
	total := len(allIDs)

	log.Info("extract-wav-clips: starting",
		"total_books", total, "cache_dir", cacheDir, "skip_existing", skipExisting)

	pages := chunkIDs(allIDs, extractWAVPageSize)

	// hashFailed and hashMismatched are counted separately from failed: clip
	// extraction can succeed while the identity write fails, and a summary that
	// folds those together reports "0 failed" for a run that wrote no hashes.
	var extracted, skipped, failed int
	var hashFailed, hashMismatched int

	err = registry.RunItems(ctx, reporter, pages, func(ctx context.Context, ids []string) error {
		sem := make(chan struct{}, introTranscribeFFWorkers)
		var mu sync.Mutex
		var wg sync.WaitGroup

		for _, bookID := range ids {
			b, gerr := store.GetBookByID(bookID)
			if gerr != nil || b == nil {
				continue
			}
			ref, _ := firstAudioFile(store, *b)
			src, cacheKey := ref.Path, ref.CacheKey
			if src == "" || cacheKey == "" {
				continue
			}
			dest := cachedClipPath(cacheDir, cacheKey)

			if skipExisting {
				if _, statErr := os.Stat(dest); statErr == nil {
					mu.Lock()
					skipped++
					mu.Unlock()
					continue
				}
			}

			wg.Go(func() {
				sem <- struct{}{}
				defer func() { <-sem }()

				ffCmd := exec.CommandContext(ctx, "ffmpeg",
					"-y", "-i", src,
					"-t", "90",
					"-vn", "-ar", "16000", "-ac", "1", "-f", "wav",
					dest,
				)
				out, ferr := ffCmd.CombinedOutput()

				mu.Lock()
				defer mu.Unlock()

				if ferr != nil {
					log.Warn("extract-wav-clips: ffmpeg failed",
						"book_id", bookID, "file", src,
						"err", ferr, "output", strings.TrimSpace(string(out)))
					failed++
					return
				}

				extracted++

				// Hash the extracted WAV clip (small: 90s × 16kHz × 2B ≈ 2.9MB).
				wavHash, werr := hashFileSHA256(dest)
				if werr != nil {
					// Logged, not just blanked: an empty wav_sha256 in the
					// journal with no cause is unreadable six months on.
					log.Warn("extract-wav-clips: clip hash failed",
						"book_id", bookID, "clip", dest, "err", werr)
					wavHash = ""
				}

				// Content-addressed identity for the source file. See
				// persistCanonicalFileHash for why this must be the canonical
				// digest and why the write-back is when-missing only.
				srcHash, herr := persistCanonicalFileHash(store, cacheDir, ref, dest)
				if herr != nil {
					log.Warn("extract-wav-clips: source hash/persist failed",
						"book_id", bookID, "file", src, "err", herr)
					hashFailed++
				}

				// Free census for TODO-FILEHASH-REPAIR. This op recomputes the
				// canonical digest for every book it touches anyway, so a row
				// whose stored hash disagrees is positively identified as
				// corrupted at zero extra I/O. Do NOT write — repair must be a
				// deliberate op — but do COUNT: the repair task cannot be sized
				// without a number, and throwing the observation away here means
				// paying for the same full-library read a second time.
				if srcHash != "" && ref.StoredHash != "" && ref.StoredHash != srcHash {
					log.Warn("extract-wav-clips: stored file_hash disagrees with the canonical digest",
						"book_id", bookID, "book_file_id", ref.BookFileID,
						"stored", ref.StoredHash, "canonical", srcHash, "file", src)
					hashMismatched++
				}

				log.Info("extract-wav-clips: extracted",
					"book_id", bookID,
					"src_sha256", srcHash,
					"wav_sha256", wavHash,
					"file", src)
			})
		}
		wg.Wait()

		_ = reporter.UpdateProgress(extracted+skipped, total,
			fmt.Sprintf("WAV clips: %d extracted, %d skipped (cached), %d failed, %d hash failed, %d hash mismatched",
				extracted, skipped, failed, hashFailed, hashMismatched))
		return nil
	}, registry.RunItemsOptions{
		Concurrency:   1,
		ProgressTotal: len(pages),
		Label: func(i, t int) string {
			return fmt.Sprintf("Page %d/%d — %d extracted, %d skipped", i+1, t, extracted, skipped)
		},
	})
	if err != nil {
		return err
	}

	log.Info("extract-wav-clips: complete",
		"extracted", extracted, "skipped", skipped, "failed", failed,
		"hash_failed", hashFailed, "hash_mismatched", hashMismatched, "total", total)
	_ = reporter.UpdateProgress(1, 1,
		fmt.Sprintf("Done — %d extracted, %d already cached, %d failed, %d hash failed, %d hash mismatched",
			extracted, skipped, failed, hashFailed, hashMismatched))
	return nil
}

// fileHashSetter is the narrow store surface persistCanonicalFileHash needs.
// Kept separate from the plugin's full store interface so the invariant below
// can be tested without a database.
type fileHashSetter interface {
	SetBookFileHash(id, hash string) error
}

// persistCanonicalFileHash computes ref.Path's identity digest, hardlinks the
// just-extracted clip under that content-addressed name, and writes the digest
// back to book_files.file_hash ONLY when the row has none. It returns the
// digest so the caller can log it.
//
// Two rules are load-bearing here, and this op previously broke both.
//
// 1. The digest MUST be filehash.BookFileHash. book_files.file_hash is an
// identity column: internal/dedup/collectors_exact.go emits SigExactFile at
// Confidence 1.0 — certainty — for two books sharing a value in it. This op
// used to write a plain whole-file SHA-256, which above 100 MB can never equal
// the digest the scanner and the backfill job write. Two byte-identical files
// then hash to two different strings and the collector simply never fires: no
// error, no log, just duplicates that are never found. The corrupted rows were
// exactly the >100 MB population the chunked strategy exists for.
//
// 2. The write-back MUST be when-missing only. It is what this op's
// Description has always promised and what the sibling
// internal/maintenance/jobs backfill_file_hashes.go job does (`if bf.FileHash
// != "" { continue }`). Clip extraction is a CONSUMER of file identity, not
// its owner; a row whose stored hash disagrees with the canonical digest is
// corrupted and needs a repair op that recomputes deliberately, not a silent
// overwrite as a side effect of caching a WAV.
func persistCanonicalFileHash(store fileHashSetter, cacheDir string, ref audioFileRef, clipPath string) (string, error) {
	srcHash, err := filehash.BookFileHash(ref.Path)
	if err != nil {
		return "", fmt.Errorf("hash source %s: %w", ref.Path, err)
	}
	if srcHash == "" {
		// Unreachable: BookFileHash returns a 64-char hex string on every
		// non-error path. Loud rather than a silent success, because as a
		// `return "", nil` the caller's `herr != nil` check would not fire and
		// an empty digest would be logged as though it were one.
		return "", fmt.Errorf("filehash returned an empty digest for %s", ref.Path)
	}

	// Hardlink the clip under its content-addressed name — zero extra disk
	// space, and it survives an organize rename because the name comes from
	// content, not location. Done even when the row already carries a DIFFERENT
	// hash (a row corrupted by an earlier run of this op): the link is free and
	// warms the cache for the name a repaired row will use.
	if ref.CacheKey != srcHash && clipPath != "" {
		if contentDest := cachedClipPath(cacheDir, srcHash); contentDest != "" {
			// Link unconditionally and forgive only ErrExist. The previous
			// os.Stat/IsNotExist gate was wrong twice: it is a TOCTOU against
			// the sibling workers in this same fan-out, and every non-ENOENT
			// stat error (EACCES, EIO, ELOOP) made IsNotExist false, so a
			// broken cache directory looked exactly like a warm cache. ErrExist
			// is the one error that means "another worker got here first";
			// anything else (EXDEV, EPERM on an SMB/FUSE mount that refuses
			// hardlinks, ENOSPC) means the content-addressed cache is not
			// working and every future run re-runs ffmpeg for this book.
			if lerr := os.Link(clipPath, contentDest); lerr != nil && !errors.Is(lerr, fs.ErrExist) {
				return srcHash, fmt.Errorf("hardlink clip %s -> %s: %w", clipPath, contentDest, lerr)
			}
		}
	}

	if ref.BookFileID == "" || ref.StoredHash != "" {
		return srcHash, nil
	}
	if err := store.SetBookFileHash(ref.BookFileID, srcHash); err != nil {
		return srcHash, fmt.Errorf("persist file hash for %s: %w", ref.BookFileID, err)
	}
	return srcHash, nil
}

// hashFileSHA256 returns the hex-encoded SHA-256 digest of the file at path.
func hashFileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
