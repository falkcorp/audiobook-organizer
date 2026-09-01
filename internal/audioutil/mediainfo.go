// file: internal/audioutil/mediainfo.go
// version: 1.0.0
// guid: 9034c2e0-67ac-4ac2-9685-199093ff8406
// last-edited: 2026-08-31

package audioutil

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

// ErrMediaInfoNotAvailable is returned by LookupMediaInfo when no mediainfo
// binary can be resolved. Unlike ErrFFprobeNotAvailable this is NOT a
// refuse-to-run condition: mediainfo is an accelerator in front of ffprobe, and
// its absence simply means every probe takes the ffprobe path.
var ErrMediaInfoNotAvailable = errors.New("audioutil: mediainfo not found on PATH")

// disableMediaInfoEnv is a kill switch. This changes the tool used by every
// duration probe in the process, on a hot path that runs across the whole
// library, so there has to be a way to revert to ffprobe-only without a
// rebuild. Set ABK_DISABLE_MEDIAINFO=1 to force the ffprobe path.
const disableMediaInfoEnv = "ABK_DISABLE_MEDIAINFO"

// lookupMediaInfoOnce caches the PATH search. ProbeDurationSeconds is called
// once per file across tens of thousands of files, and re-running a PATH search
// each time is exactly the per-file cost this package already avoids for
// ffprobe by having callers resolve the path up front.
var lookupMediaInfoOnce = sync.OnceValues(func() (string, error) {
	p, err := exec.LookPath("mediainfo")
	if err != nil {
		return "", ErrMediaInfoNotAvailable
	}
	return p, nil
})

// mediaInfoDisabled reads the kill switch. Deliberately NOT folded into the
// sync.OnceValues above: caching the PATH lookup is a performance measure, but
// caching the SWITCH would make it un-flippable for the life of the process and
// untestable without a subprocess.
func mediaInfoDisabled() bool {
	v := strings.TrimSpace(os.Getenv(disableMediaInfoEnv))
	return v != "" && v != "0" && !strings.EqualFold(v, "false")
}

// LookupMediaInfo resolves the mediainfo binary on PATH, honouring the kill
// switch.
func LookupMediaInfo() (string, error) {
	if mediaInfoDisabled() {
		return "", ErrMediaInfoNotAvailable
	}
	return lookupMediaInfoOnce()
}

// MediaInfoAvailable reports whether LookupMediaInfo would succeed.
func MediaInfoAvailable() bool {
	_, err := LookupMediaInfo()
	return err == nil
}

// probeDurationMediaInfo returns the container duration in seconds using
// mediainfo, which reports it in MILLISECONDS via the %Duration% template.
//
// SILENT-FAILURE HAZARD, and the reason this function validates so hard:
// mediainfo exits 0 with EMPTY stdout for a file it cannot parse AND for a file
// that does not exist. Measured with MediaInfoLib v26.05:
//
//	mediainfo --Output='General;%Duration%' /tmp/notaudio.txt  -> rc=0, ""
//	mediainfo --Output='General;%Duration%' /tmp/missing.m4b   -> rc=0, ""
//
// So the exit status carries no information here and must not be trusted on its
// own. Anything that is not a positive number is an ERROR, never a zero
// duration: returning (0, nil) would mark every unreadable file as a
// successfully-probed zero-length one, and the caller's fallback to ffprobe --
// which does report a real failure -- would never run.
func probeDurationMediaInfo(ctx context.Context, mediaInfoPath, filePath string) (float64, error) {
	bin := mediaInfoPath
	if bin == "" {
		bin = "mediainfo"
	}
	cmd := exec.CommandContext(ctx, bin, "--Output=General;%Duration%", filePath)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("mediainfo %s: %w", filePath, err)
	}

	raw := strings.TrimSpace(stdout.String())
	if raw == "" {
		return 0, fmt.Errorf("mediainfo %s: empty duration (unreadable or unsupported file)", filePath)
	}
	// Some builds emit a human string ("1 h 2 min") rather than milliseconds for
	// %Duration%. Parse strictly; anything non-numeric falls through to ffprobe
	// rather than being coerced.
	ms, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("mediainfo %s: non-numeric duration %q: %w", filePath, raw, err)
	}
	if ms <= 0 {
		return 0, fmt.Errorf("mediainfo %s: non-positive duration %v", filePath, ms)
	}
	return ms / 1000.0, nil
}
