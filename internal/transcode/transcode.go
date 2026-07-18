// file: internal/transcode/transcode.go
// version: 1.6.0
// guid: f8a1b2c3-d4e5-6789-abcd-ef0123456789
// last-edited: 2026-07-18

package transcode

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/audioutil"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
)

// TranscodeOpts configures a transcode operation.
type TranscodeOpts struct {
	BookID       string
	OutputFormat string // "m4b" default
	Bitrate      int    // kbps, default 128
	KeepOriginal bool   // default true
}

// FindFFmpeg locates ffmpeg on the system PATH.
func FindFFmpeg() (string, error) {
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		return "", fmt.Errorf("ffmpeg not found on PATH: %w", err)
	}
	return path, nil
}

// FindFFprobe locates ffprobe on the system PATH.
func FindFFprobe() (string, error) {
	path, err := exec.LookPath("ffprobe")
	if err != nil {
		return "", fmt.Errorf("ffprobe not found on PATH: %w", err)
	}
	return path, nil
}

// CollectInputFiles gathers audio files for a book, sorted by track number.
// If the book has files, those are used; otherwise the book's file_path is used.
func CollectInputFiles(book *database.Book, files []database.BookFile) ([]string, error) {
	if len(files) > 0 {
		// Sort by track number, then by file path as tiebreaker
		sort.Slice(files, func(i, j int) bool {
			if files[i].TrackNumber != files[j].TrackNumber {
				return files[i].TrackNumber < files[j].TrackNumber
			}
			return files[i].FilePath < files[j].FilePath
		})
		var paths []string
		for _, f := range files {
			if f.Missing {
				continue
			}
			if _, err := os.Stat(f.FilePath); err != nil {
				return nil, fmt.Errorf("book file not found: %s: %w", f.FilePath, err)
			}
			paths = append(paths, f.FilePath)
		}
		if len(paths) == 0 {
			return nil, fmt.Errorf("no active files found for book %s", book.ID)
		}
		return paths, nil
	}

	if book.FilePath == "" {
		return nil, fmt.Errorf("book %s has no file_path", book.ID)
	}
	if _, err := os.Stat(book.FilePath); err != nil {
		return nil, fmt.Errorf("book file not found: %s: %w", book.FilePath, err)
	}
	return []string{book.FilePath}, nil
}

// BuildConcatFile writes an ffmpeg concat demuxer file listing the input files.
// Returns the path to the temp file (caller must clean up).
func BuildConcatFile(inputFiles []string) (string, error) {
	f, err := os.CreateTemp("", "audiobook-concat-*.txt")
	if err != nil {
		return "", fmt.Errorf("failed to create concat file: %w", err)
	}
	for _, path := range inputFiles {
		// Escape single quotes for ffmpeg concat format
		escaped := strings.ReplaceAll(path, "'", "'\\''")
		if _, err := fmt.Fprintf(f, "file '%s'\n", escaped); err != nil {
			f.Close()
			os.Remove(f.Name())
			return "", fmt.Errorf("failed to write concat file: %w", err)
		}
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// probeDuration returns the duration of an audio file in seconds using
// ffprobe. This is a thin wrapper over the shared
// audioutil.ProbeDurationSeconds (also used by internal/mediainfo and
// internal/fingerprint — see TODO item 20 / AP-3b); it used to parse ffprobe's
// JSON output directly, but that produced the same duration value as the
// shared plain-text probe, so the duplicate implementation was removed.
func probeDuration(ffprobePath, filePath string) (float64, error) {
	return audioutil.ProbeDurationSeconds(context.Background(), ffprobePath, filePath)
}

// BuildChapterMetadata probes each input file and generates an FFMetadata chapter file.
// Returns the path to the temp metadata file (caller must clean up).
func BuildChapterMetadata(inputFiles []string) (string, error) {
	ffprobePath, err := FindFFprobe()
	if err != nil {
		return "", err
	}
	return BuildChapterMetadataWithProber(inputFiles, func(path string) (float64, error) {
		return probeDuration(ffprobePath, path)
	})
}

// BuildChapterMetadataWithProber generates chapter metadata using a custom duration prober.
// This is useful for testing.
func BuildChapterMetadataWithProber(inputFiles []string, prober func(string) (float64, error)) (string, error) {
	f, err := os.CreateTemp("", "audiobook-chapters-*.txt")
	if err != nil {
		return "", fmt.Errorf("failed to create chapter file: %w", err)
	}

	if _, err := fmt.Fprintln(f, ";FFMETADATA1"); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}

	var offsetMs int64
	for i, path := range inputFiles {
		dur, err := prober(path)
		if err != nil {
			f.Close()
			os.Remove(f.Name())
			return "", err
		}
		durationMs := int64(dur * 1000)
		title := fmt.Sprintf("Chapter %d", i+1)

		if _, err := fmt.Fprintf(f, "\n[CHAPTER]\nTIMEBASE=1/1000\nSTART=%d\nEND=%d\ntitle=%s\n",
			offsetMs, offsetMs+durationMs, title); err != nil {
			f.Close()
			os.Remove(f.Name())
			return "", err
		}
		offsetMs += durationMs
	}

	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// Transcode converts audio files for a book into a single M4B.
func Transcode(ctx context.Context, opts TranscodeOpts, store interface {
	database.BookReader
	database.BookFileStore
}, progress operations.ProgressReporter) (string, error) {
	ffmpegPath, err := FindFFmpeg()
	if err != nil {
		return "", err
	}

	if opts.OutputFormat == "" {
		opts.OutputFormat = "m4b"
	}
	if opts.Bitrate <= 0 {
		opts.Bitrate = 128
	}

	progress.UpdateProgress(0, 5, "Loading book data")

	book, err := store.GetBookByID(opts.BookID)
	if err != nil {
		return "", fmt.Errorf("failed to get book: %w", err)
	}

	// Get files for multi-file books
	bookFiles, filesErr := store.GetBookFiles(book.ID)
	if filesErr != nil {
		progress.Log("warn", fmt.Sprintf("Could not fetch book files: %v, falling back to book file path", filesErr), nil)
		bookFiles = nil
	} else {
		progress.Log("info", fmt.Sprintf("Found %d files for book %s", len(bookFiles), book.ID), nil)
	}

	inputFiles, err := CollectInputFiles(book, bookFiles)
	if err != nil {
		progress.Log("error", fmt.Sprintf("Failed to collect input files: %v", err), nil)
		return "", fmt.Errorf("failed to collect input files: %w", err)
	}

	multiFile := len(inputFiles) > 1
	progress.Log("info", fmt.Sprintf("Transcoding %d input file(s) to M4B", len(inputFiles)), nil)
	for i, f := range inputFiles {
		progress.Log("info", fmt.Sprintf("  Input %d: %s", i+1, f), nil)
	}

	// Determine output path: same directory, .m4b extension
	baseDir := filepath.Dir(inputFiles[0])
	baseName := strings.TrimSuffix(filepath.Base(book.FilePath), filepath.Ext(book.FilePath))
	if baseName == "" {
		baseName = book.Title
	}
	outputPath := filepath.Join(baseDir, baseName+".m4b")
	tmpOutput := filepath.Join(baseDir, baseName+"-transcode.tmp.m4b")

	// Track all temp files for cleanup on failure
	tempFiles := []string{tmpOutput}
	success := false
	defer func() {
		if !success {
			for _, f := range tempFiles {
				if err := os.Remove(f); err == nil {
					slog.Info("transcode cleaned up temp file", "file", f)
				}
			}
		}
	}()

	progress.UpdateProgress(1, 5, "Preparing transcode")

	var args []string

	// Compute total duration (microseconds) for progress reporting
	var totalDurationUs int64
	for _, f := range bookFiles {
		totalDurationUs += int64(f.Duration) * 1_000 // BookFile stores ms
	}
	if totalDurationUs == 0 && book.Duration != nil && *book.Duration > 0 {
		totalDurationUs = int64(*book.Duration) * 1_000_000
	}
	// Fallback: probe input files with ffprobe if we still have no duration
	if totalDurationUs == 0 {
		for _, f := range inputFiles {
			if dur := probeFileDuration(f); dur > 0 {
				totalDurationUs += dur
			}
		}
	}
	progress.Log("info", fmt.Sprintf("Total duration for progress: %d us (from %d files)", totalDurationUs, len(bookFiles)), nil)

	if multiFile {
		// Build concat file
		concatFile, err := BuildConcatFile(inputFiles)
		if err != nil {
			return "", err
		}
		defer os.Remove(concatFile)

		args = []string{
			"-y",
			"-f", "concat",
			"-safe", "0",
			"-i", concatFile,
			"-c:a", "aac",
			"-b:a", fmt.Sprintf("%dk", opts.Bitrate),
			"-movflags", "+faststart",
			"-progress", "pipe:1",
			tmpOutput,
		}
	} else {
		// Single file transcode
		args = []string{
			"-y",
			"-i", inputFiles[0],
			"-c:a", "aac",
			"-b:a", fmt.Sprintf("%dk", opts.Bitrate),
			"-movflags", "+faststart",
			"-progress", "pipe:1",
			tmpOutput,
		}
	}

	progress.UpdateProgress(2, 5, "Transcoding audio")
	progress.Log("info", fmt.Sprintf("Running ffmpeg: %s %s", ffmpegPath, strings.Join(args, " ")), nil)

	cmd := exec.CommandContext(ctx, ffmpegPath, args...)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("failed to create stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("failed to start ffmpeg: %w", err)
	}

	// Capture stderr in background
	var stderrBuf strings.Builder
	go func() {
		scanner := bufio.NewScanner(stderrPipe)
		for scanner.Scan() {
			stderrBuf.WriteString(scanner.Text())
			stderrBuf.WriteString("\n")
		}
	}()

	// Parse stdout progress lines
	scanner := bufio.NewScanner(stdoutPipe)
	for scanner.Scan() {
		line := scanner.Text()
		if val, ok := strings.CutPrefix(line, "out_time_ms="); ok {
			if us, err := strconv.ParseInt(val, 10, 64); err == nil && totalDurationUs > 0 {
				pct := min(int(us*100/totalDurationUs), 100)
				progress.UpdateProgress(2, 5, fmt.Sprintf("Transcoding audio (%d%%)", pct))
			}
		}
	}

	if err := cmd.Wait(); err != nil {
		errMsg := fmt.Sprintf("ffmpeg transcode failed: %v", err)
		outputStr := stderrBuf.String()
		progress.Log("error", errMsg, &outputStr)
		return "", fmt.Errorf("ffmpeg transcode failed: %w\noutput: %s", err, outputStr)
	}
	progress.Log("info", "FFmpeg transcode completed successfully", nil)

	// Mux chapter metadata if multi-file
	if multiFile {
		progress.UpdateProgress(3, 5, "Adding chapter markers")

		chapterFile, err := BuildChapterMetadata(inputFiles)
		if err != nil {
			slog.Warn("transcode failed to build chapter metadata, skipping", "err", err)
		} else {
			defer os.Remove(chapterFile)

			chapteredOutput := outputPath + ".ch.m4b"
			tempFiles = append(tempFiles, chapteredOutput)
			chapterArgs := []string{
				"-y",
				"-i", tmpOutput,
				"-i", chapterFile,
				"-map_metadata", "1",
				"-map_chapters", "1",
				"-c", "copy",
				chapteredOutput,
			}
			chapterCmd := exec.CommandContext(ctx, ffmpegPath, chapterArgs...)
			chOut, err := chapterCmd.CombinedOutput()
			if err != nil {
				slog.Warn("transcode chapter muxing failed, using unchaptered output", "err", err, "ffmpegOutput", string(chOut))
			} else {
				os.Remove(tmpOutput)
				tmpOutput = chapteredOutput
			}
		}
	}

	progress.UpdateProgress(4, 5, "Finalizing")

	// Embed cover art if available
	if book.CoverURL != nil && *book.CoverURL != "" {
		coverPath := *book.CoverURL
		if _, err := os.Stat(coverPath); err == nil {
			coverOutput := tmpOutput + ".cover.m4b"
			tempFiles = append(tempFiles, coverOutput)
			coverArgs := []string{
				"-y",
				"-i", tmpOutput,
				"-i", coverPath,
				"-map", "0", "-map", "1",
				"-c", "copy",
				"-disposition:v:0", "attached_pic",
				"-movflags", "+faststart",
				coverOutput,
			}
			coverCmd := exec.CommandContext(ctx, ffmpegPath, coverArgs...)
			if coverOut, err := coverCmd.CombinedOutput(); err != nil {
				slog.Warn("transcode cover art embedding failed", "err", err, "ffmpegOutput", string(coverOut))
			} else {
				os.Remove(tmpOutput)
				tmpOutput = coverOutput
			}
		}
	}

	// Atomic rename to final path
	if err := os.Rename(tmpOutput, outputPath); err != nil {
		return "", fmt.Errorf("failed to finalize output file: %w", err)
	}

	success = true
	progress.UpdateProgress(5, 5, "Complete")
	progress.Log("info", fmt.Sprintf("Transcode complete: %s → %s", book.FilePath, outputPath), nil)

	slog.Info("transcode completed", "book", book.FilePath, "outputPath", outputPath)
	return outputPath, nil
}

// probeFileDuration uses ffprobe to get a file's duration in microseconds.
// Thin wrapper over the shared audioutil.ProbeDurationSeconds (see
// probeDuration above and TODO item 20 / AP-3b) — converts the shared
// seconds-float result to this call site's historical microsecond-int64
// return, and preserves the original best-effort "0 on any failure" contract
// (no error is surfaced; callers treat 0 as "could not determine").
func probeFileDuration(filePath string) int64 {
	ffprobePath, err := exec.LookPath("ffprobe")
	if err != nil {
		return 0
	}
	seconds, err := audioutil.ProbeDurationSeconds(context.Background(), ffprobePath, filePath)
	if err != nil {
		return 0
	}
	return int64(seconds * 1_000_000)
}

// CleanupStaleTempFiles removes transcode temp files older than maxAge from the given directory tree.
// Call this periodically (e.g. on server start and via a ticker) to catch orphans from crashed transcodes.
func CleanupStaleTempFiles(rootDir string, maxAge time.Duration) int {
	cleaned := 0
	_ = filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		name := info.Name()
		if strings.Contains(name, "-transcode.tmp") || strings.HasSuffix(name, ".ch.m4b") {
			if time.Since(info.ModTime()) > maxAge {
				if err := os.Remove(path); err == nil {
					slog.Info("transcode cleaned up stale temp file", "path", path, "age", time.Since(info.ModTime()).Round(time.Minute))
					cleaned++
				}
			}
		}
		return nil
	})
	return cleaned
}

// StartCleanupTicker runs periodic temp file cleanup. Returns a stop function.
func StartCleanupTicker(rootDir string, interval, maxAge time.Duration) func() {
	ticker := time.NewTicker(interval)
	done := make(chan struct{})
	go func() {
		// Run once immediately on start
		if n := CleanupStaleTempFiles(rootDir, maxAge); n > 0 {
			slog.Info("transcode startup cleanup removed stale temp files", "count", n)
		}
		for {
			select {
			case <-ticker.C:
				if n := CleanupStaleTempFiles(rootDir, maxAge); n > 0 {
					slog.Info("transcode periodic cleanup removed stale temp files", "count", n)
				}
			case <-done:
				ticker.Stop()
				return
			}
		}
	}()
	return func() { close(done) }
}
