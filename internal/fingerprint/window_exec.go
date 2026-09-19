// file: internal/fingerprint/window_exec.go
// version: 1.1.0
// guid: 06d35c35-707c-4690-9bdb-eb99cb5f2002
// last-edited: 2026-09-19

package fingerprint

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// DefaultWindowTimeout bounds one window's ffmpeg+fpcalc run.
const DefaultWindowTimeout = 60 * time.Second

// windowStderrCap bounds how much stderr is kept from each tool.
const windowStderrCap = 4096

// windowStdoutCap bounds fpcalc's JSON output. A 120 s raw print is about
// 1,000 integers (~11 KB); a covers-whole window of a 150 s file a bit more.
const windowStdoutCap = 1 << 20

// windowMinDecodedFraction: a window whose decoded PCM is shorter than this
// fraction of its planned length was cut past the real end of the audio (the
// duration used to plan it was wrong), so it does not sample the slot it
// claims to. Same 5% tolerance the design applies to remote results.
const windowMinDecodedFraction = 0.95

// Window errors. Each FileWindow failure wraps exactly one of these (or a
// context error), so a caller can pick a tombstone reason with errors.Is.
var (
	ErrWindowFFmpeg       = errors.New("fingerprint window: ffmpeg failed")
	ErrWindowFpcalc       = errors.New("fingerprint window: fpcalc failed")
	ErrWindowParse        = errors.New("fingerprint window: cannot parse fpcalc output")
	ErrWindowShortDecode  = errors.New("fingerprint window: decoded audio shorter than the planned window")
	ErrWindowToolsMissing = errors.New("fingerprint window: ffmpeg and fpcalc paths are both required")

	// ErrWindowTransient is wrapped ALONGSIDE ErrWindowFFmpeg/ErrWindowFpcalc
	// when the failure says nothing about the file: the process could not
	// start (fork EAGAIN/ENOMEM, a missing binary), it died of a signal while
	// the context was live (the OOM killer), or the tool reported a
	// filesystem I/O error (EIO/ESTALE on NFS or ZFS). A caller that records
	// durable failures must not record one of these.
	ErrWindowTransient = errors.New("fingerprint window: transient failure")
)

// transientIOMarkers are strerror texts a tool prints when the READ failed
// rather than the decode: the bytes may be fine next time.
var transientIOMarkers = []string{
	"Input/output error",
	"Stale file handle",
	"Stale NFS file handle",
	"Resource temporarily unavailable",
	"Cannot allocate memory",
	"Transport endpoint is not connected",
	"No such device",
}

// toolFailure wraps a tool's Wait error: always sentinel, plus
// ErrWindowTransient for a signal death or an I/O-error message.
func toolFailure(sentinel error, stderr *cappedBuffer, waitErr error) error {
	msg := toolMsg(stderr, waitErr)
	var ee *exec.ExitError
	if errors.As(waitErr, &ee) && ee.ExitCode() == -1 {
		// ExitCode is -1 when the process was terminated by a signal.
		return fmt.Errorf("%w: %w: killed by signal: %s", sentinel, ErrWindowTransient, msg)
	}
	if !errors.As(waitErr, &ee) {
		// Not an exit status at all (a Wait/IO error of our own).
		return fmt.Errorf("%w: %w: %s", sentinel, ErrWindowTransient, msg)
	}
	for _, m := range transientIOMarkers {
		if strings.Contains(stderr.String(), m) {
			return fmt.Errorf("%w: %w: %s", sentinel, ErrWindowTransient, msg)
		}
	}
	return fmt.Errorf("%w: %s", sentinel, msg)
}

// ToolResolver is the part of tools.ToolRegistry this package needs.
// *tools.ToolRegistry satisfies it; ffmpeg must be registered on it
// (tools.ToolDef{Name: "ffmpeg"}, system mode) for Resolve to succeed.
type ToolResolver interface {
	Resolve(name string) (string, error)
}

// ToolVersionInfo is the version string of each tool, recorded with every
// window so prints from different tool builds are never compared unknowingly.
type ToolVersionInfo struct {
	Fpcalc string `json:"fpcalc_version"`
	FFmpeg string `json:"ffmpeg_version"`
}

// WindowTools is everything FileWindow needs, passed by value: no package
// state is read, so two runners with different binaries can coexist.
type WindowTools struct {
	FpcalcPath string
	FFmpegPath string
	// Versions are stamped onto every WindowPrint.
	Versions ToolVersionInfo
	// Timeout per window; 0 means DefaultWindowTimeout.
	Timeout time.Duration
}

// ResolveWindowTools resolves fpcalc and ffmpeg through r (the server's
// ToolRegistry) and queries both versions once.
func ResolveWindowTools(ctx context.Context, r ToolResolver) (WindowTools, error) {
	fp, err := r.Resolve("fpcalc")
	if err != nil {
		return WindowTools{}, fmt.Errorf("resolve fpcalc: %w", err)
	}
	ff, err := r.Resolve("ffmpeg")
	if err != nil {
		return WindowTools{}, fmt.Errorf("resolve ffmpeg: %w", err)
	}
	v, err := ToolVersions(ctx, fp, ff)
	if err != nil {
		return WindowTools{}, err
	}
	return WindowTools{FpcalcPath: fp, FFmpegPath: ff, Versions: v}, nil
}

// ToolVersions runs `fpcalc -version` and `ffmpeg -version` and returns the
// version token of each ("1.6.1", "8.0.1"). An unparseable answer is an
// error: an empty version must never be stored as if it were one.
func ToolVersions(ctx context.Context, fpcalcPath, ffmpegPath string) (ToolVersionInfo, error) {
	fp, err := toolVersion(ctx, fpcalcPath, "fpcalc")
	if err != nil {
		return ToolVersionInfo{}, err
	}
	ff, err := toolVersion(ctx, ffmpegPath, "ffmpeg")
	if err != nil {
		return ToolVersionInfo{}, err
	}
	return ToolVersionInfo{Fpcalc: fp, FFmpeg: ff}, nil
}

func toolVersion(ctx context.Context, bin, name string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "-version").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s -version: %w", name, err)
	}
	v := parseToolVersion(string(out), name)
	if v == "" {
		first, _, _ := strings.Cut(string(out), "\n")
		return "", fmt.Errorf("%s -version: no version in %q", name, first)
	}
	return v, nil
}

// parseToolVersion returns the token after "<name> version " on the first
// line that has it: "fpcalc version 1.6.1 (FFmpeg ...)" -> "1.6.1",
// "ffmpeg version 8.0.1 Copyright ..." -> "8.0.1".
func parseToolVersion(out, name string) string {
	marker := name + " version "
	for line := range strings.SplitSeq(out, "\n") {
		_, rest, ok := strings.Cut(strings.TrimSpace(line), marker)
		if !ok {
			continue
		}
		if f := strings.Fields(rest); len(f) > 0 {
			return f[0]
		}
	}
	return ""
}

// WindowPrint is one computed window, in memory. It carries the fields of the
// design's database.FingerprintWindow that the fingerprint package owns;
// storage fields (ref, source size/mtime, host, lease, computed-at) are added
// by the caller that persists it.
type WindowPrint struct {
	Kind            WindowKind
	SlotBP          int
	WindowSet       string
	OffsetSec       float64
	LengthSec       float64
	DecodedSec      float64
	CoversWhole     bool
	DurationUsedSec float64
	DurationSource  DurationSource
	Frames          int
	// Raw is the uncompressed chromaprint (fpcalc -raw), little-endian
	// uint32 per frame. NOTE: do not Hamming-compare it with the legacy
	// BookFile.AcoustIDFingerprint. Rows of that field written before
	// 2026-09-19 hold fpcalc's compressed output misread as uint32s (not
	// frames at all), and rows written since hold real frames from a
	// different pipeline (fpcalc reading the file directly).
	Raw           []byte
	Algorithm     int
	Pipeline      string
	FpcalcVersion string
	FFmpegVersion string
}

// FramesPerSec is Chromaprint's frame rate at 11025 Hz: one frame per
// 4096/3 samples.
const FramesPerSec = float64(WindowSampleRate) / (4096.0 / 3.0)

// windowFFmpegArgs is the ffmpeg half of WindowPipelineID. -ss precedes -i
// (input seek); the path goes through the file: protocol so a ':' in a name
// is not read as a protocol prefix.
func windowFFmpegArgs(path string, offsetSec, lengthSec float64) []string {
	return []string{
		"-nostdin", "-hide_banner", "-v", "error",
		"-ss", strconv.FormatFloat(offsetSec, 'f', 3, 64),
		"-i", "file:" + path,
		"-t", strconv.FormatFloat(lengthSec, 'f', 3, 64),
		"-map", "0:a:0", "-vn",
		"-ac", "1", "-ar", strconv.Itoa(WindowSampleRate),
		"-f", "s16le", "pipe:1",
	}
}

// windowFpcalcArgs is the fpcalc half of WindowPipelineID. -raw makes fpcalc
// print the frames as a JSON integer array, which parseRawFpcalcJSON reads
// directly; the default output is Chromaprint's compressed form, which would
// need decompressChromaprint first.
func windowFpcalcArgs(lengthSec float64) []string {
	return []string{
		"-format", "s16le",
		"-rate", strconv.Itoa(WindowSampleRate),
		"-channels", "1",
		"-length", strconv.Itoa(int(math.Ceil(lengthSec))),
		"-algorithm", strconv.Itoa(WindowAlgorithm),
		"-raw", "-json", "-",
	}
}

// FileWindow cuts one window out of the seekable file at path and
// fingerprints it: ffmpeg decodes spec.LengthSec seconds from spec.OffsetSec
// to 11025 Hz mono s16le PCM, and fpcalc fingerprints that PCM from stdin.
// Only PCM crosses the pipe, never a container.
//
// It fails (never returns a partial print) when ffmpeg or fpcalc exits
// non-zero, when fpcalc's output does not parse, when the print has fewer
// than MinUsefulFingerprintFrames frames (ErrFingerprintTooShort), when the
// decoded audio is under 95% of the planned length, or when ctx is cancelled
// or the per-window timeout expires. Both processes are killed on
// cancellation.
func (t WindowTools) FileWindow(ctx context.Context, path string, spec WindowSpec) (*WindowPrint, error) {
	if t.FpcalcPath == "" || t.FFmpegPath == "" {
		return nil, ErrWindowToolsMissing
	}
	if spec.LengthSec <= 0 || spec.OffsetSec < 0 {
		return nil, fmt.Errorf("fingerprint window: invalid spec offset=%.3f length=%.3f", spec.OffsetSec, spec.LengthSec)
	}
	timeout := t.Timeout
	if timeout <= 0 {
		timeout = DefaultWindowTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	frames, pcmBytes, err := runWindowPipe(ctx, t.FFmpegPath, t.FpcalcPath,
		windowFFmpegArgs(path, spec.OffsetSec, spec.LengthSec), windowFpcalcArgs(spec.LengthSec))
	if err != nil {
		return nil, fmt.Errorf("%s @%.3fs+%.3fs: %w", path, spec.OffsetSec, spec.LengthSec, err)
	}
	if len(frames) < MinUsefulFingerprintFrames {
		return nil, fmt.Errorf("%s @%.3fs: %w: %d frames (< %d)", path, spec.OffsetSec,
			ErrFingerprintTooShort, len(frames), MinUsefulFingerprintFrames)
	}
	decoded := float64(pcmBytes) / (2 * WindowSampleRate)
	if decoded < spec.LengthSec*windowMinDecodedFraction {
		return nil, fmt.Errorf("%s @%.3fs: %w: %.3fs of %.3fs", path, spec.OffsetSec,
			ErrWindowShortDecode, decoded, spec.LengthSec)
	}

	raw := make([]byte, len(frames)*4)
	for i, f := range frames {
		binary.LittleEndian.PutUint32(raw[i*4:], f)
	}
	return &WindowPrint{
		Kind:            spec.Kind,
		SlotBP:          spec.SlotBP,
		WindowSet:       spec.WindowSet,
		OffsetSec:       spec.OffsetSec,
		LengthSec:       spec.LengthSec,
		DecodedSec:      decoded,
		CoversWhole:     spec.CoversWhole,
		DurationUsedSec: spec.Duration.Sec,
		DurationSource:  spec.Duration.Source,
		Frames:          len(frames),
		Raw:             raw,
		Algorithm:       WindowAlgorithm,
		Pipeline:        WindowPipelineID,
		FpcalcVersion:   t.Versions.Fpcalc,
		FFmpegVersion:   t.Versions.FFmpeg,
	}, nil
}

// runWindowPipe runs ffmpeg | fpcalc, copying the PCM through this process so
// its size can be counted (fpcalc reports duration 0 for stdin input). It
// returns fpcalc's raw frames and the number of PCM bytes ffmpeg produced.
func runWindowPipe(ctx context.Context, ffmpegPath, fpcalcPath string, ffArgs, fpArgs []string) ([]uint32, int64, error) {
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, 0, fmt.Errorf("pipe: %w", err)
	}
	defer pr.Close()

	var ffErr, fpErr, fpOut cappedBuffer
	ffErr.limit, fpErr.limit, fpOut.limit = windowStderrCap, windowStderrCap, windowStdoutCap

	ff := exec.CommandContext(ctx, ffmpegPath, ffArgs...)
	ff.Stdout = pw
	ff.Stderr = &ffErr
	ff.WaitDelay = 2 * time.Second

	fp := exec.CommandContext(ctx, fpcalcPath, fpArgs...)
	fp.Stdout = &fpOut
	fp.Stderr = &fpErr
	fp.WaitDelay = 2 * time.Second
	fpIn, err := fp.StdinPipe()
	if err != nil {
		_ = pw.Close()
		return nil, 0, fmt.Errorf("fpcalc stdin: %w", err)
	}

	if err := ff.Start(); err != nil {
		_ = pw.Close()
		return nil, 0, fmt.Errorf("%w: %w: start: %v", ErrWindowFFmpeg, ErrWindowTransient, err)
	}
	_ = pw.Close() // the child holds its own copy
	if err := fp.Start(); err != nil {
		_ = pr.Close()
		_ = ff.Process.Kill()
		_ = ff.Wait()
		return nil, 0, fmt.Errorf("%w: %w: start: %v", ErrWindowFpcalc, ErrWindowTransient, err)
	}

	// Cancellation kills both children (CommandContext), but a grandchild
	// could keep the pipe's write end open; closing the read end unblocks
	// the copy regardless.
	stop := context.AfterFunc(ctx, func() { _ = pr.Close() })
	defer stop()

	// Copy PCM to fpcalc, counting it. fpcalc stops reading at -length; if
	// its stdin closes early the rest is drained so ffmpeg never dies of
	// SIGPIPE and its exit status stays meaningful.
	counter := &countingReader{r: pr}
	_, copyErr := io.Copy(fpIn, counter)
	if copyErr != nil {
		_, _ = io.Copy(io.Discard, counter)
	}
	_ = fpIn.Close()

	ffWait := ff.Wait()
	fpWait := fp.Wait()

	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, counter.n, fmt.Errorf("fingerprint window: %w", ctxErr)
	}
	if ffWait != nil {
		return nil, counter.n, toolFailure(ErrWindowFFmpeg, &ffErr, ffWait)
	}
	if fpWait != nil {
		return nil, counter.n, toolFailure(ErrWindowFpcalc, &fpErr, fpWait)
	}
	frames, err := parseRawFpcalcJSON(fpOut.Bytes())
	if err != nil {
		return nil, counter.n, err
	}
	return frames, counter.n, nil
}

func toolMsg(stderr *cappedBuffer, err error) string {
	msg := strings.TrimSpace(stderr.String())
	if msg == "" {
		return err.Error()
	}
	return err.Error() + ": " + msg
}

// parseRawFpcalcJSON decodes `fpcalc -raw -json` output. Frames are printed as
// unsigned integers; -signed output (negative values) is accepted and
// reinterpreted, anything outside 32 bits is an error.
func parseRawFpcalcJSON(out []byte) ([]uint32, error) {
	var r struct {
		Fingerprint []json.Number `json:"fingerprint"`
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	dec.UseNumber()
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrWindowParse, err)
	}
	frames := make([]uint32, len(r.Fingerprint))
	for i, n := range r.Fingerprint {
		if u, err := strconv.ParseUint(n.String(), 10, 32); err == nil {
			frames[i] = uint32(u)
			continue
		}
		s, err := strconv.ParseInt(n.String(), 10, 32)
		if err != nil {
			return nil, fmt.Errorf("%w: frame %d %q", ErrWindowParse, i, n.String())
		}
		frames[i] = uint32(int32(s))
	}
	return frames, nil
}

// countingReader counts bytes read through it.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// cappedBuffer keeps the first limit bytes written and silently drops the
// rest, so a chatty tool cannot grow memory without bound. It reports every
// write as fully accepted so the writer never blocks or fails on it.
type cappedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if room := c.limit - c.buf.Len(); room > 0 {
		if len(p) <= room {
			c.buf.Write(p)
		} else {
			c.buf.Write(p[:room])
			c.truncated = true
		}
	} else if len(p) > 0 {
		c.truncated = true
	}
	return len(p), nil
}

func (c *cappedBuffer) Bytes() []byte { return c.buf.Bytes() }

func (c *cappedBuffer) String() string {
	if c.truncated {
		return c.buf.String() + " [truncated]"
	}
	return c.buf.String()
}
