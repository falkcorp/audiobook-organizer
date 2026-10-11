// file: internal/diagnosis/probe.go
// version: 1.1.0
// guid: d1e2f3a4-b5c6-7d8e-9f0a-1b2c3d4e5f6a
// last-edited: 2026-10-11

// Package diagnosis probes audio files with available system tools (ffprobe,
// mediainfo, file) to produce structured diagnostic data when fingerprinting
// fails. Tool availability is cached once on first use.
package diagnosis

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// FileDiagnostic holds the structured results from probing a single audio file.
// The JSON representation is stored verbatim in BookFile.FingerprintDiagnosticJSON.
type FileDiagnostic struct {
	// From file(1)
	FileMagic string `json:"file_magic,omitempty"`
	IsEmpty   bool   `json:"is_empty,omitempty"`

	// From ffprobe
	ContainerFormat  string  `json:"container_format,omitempty"`
	Codec            string  `json:"codec,omitempty"`
	DurationSec      float64 `json:"duration_sec,omitempty"`
	BitrateKbps      int     `json:"bitrate_kbps,omitempty"`
	SampleRateHz     int     `json:"sample_rate_hz,omitempty"`
	Channels         int     `json:"channels,omitempty"`
	FFProbeErrorStr  string  `json:"ffprobe_error,omitempty"`
	FFProbeErrorCode int     `json:"ffprobe_error_code,omitempty"`

	// From mediainfo
	MediaInfoFormat        string `json:"mi_format,omitempty"`
	MediaInfoFormatProfile string `json:"mi_format_profile,omitempty"`
	EncodedApplication     string `json:"encoded_application,omitempty"`
	EncodedLibrary         string `json:"encoded_library,omitempty"`
	IsStreamable           bool   `json:"is_streamable,omitempty"`
	Encryption             string `json:"encryption,omitempty"`
	TrackPosition          int    `json:"track_position,omitempty"`
	TrackTotal             int    `json:"track_total,omitempty"`
	HasCoverArt            bool   `json:"has_cover_art,omitempty"`
	HeaderSizeBytes        int64  `json:"header_size_bytes,omitempty"`
	DataSizeBytes          int64  `json:"data_size_bytes,omitempty"`

	// Derived
	HasActiveDRM     bool `json:"has_active_drm,omitempty"`
	WasOriginallyDRM bool `json:"was_originally_drm,omitempty"`
	IsTruncated      bool `json:"is_truncated,omitempty"`

	// Meta
	ToolsUsed  []string `json:"tools_used"`
	ProbeError string   `json:"probe_error,omitempty"`
}

// tools caches which executables are available. Populated once.
var (
	toolsOnce    sync.Once
	hasFFProbe   bool
	hasMediaInfo bool
	hasFileCMD   bool
)

func initTools() {
	toolsOnce.Do(func() {
		hasFFProbe = execExists("ffprobe")
		hasMediaInfo = execExists("mediainfo")
		hasFileCMD = execExists("file")
	})
}

func execExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

const (
	maxOutputBytes = 4096
	probeTimeout   = 15 * time.Second
	detailMaxBytes = 512
)

// ProbeFile runs the available tool cascade on path and returns a FileDiagnostic.
// It never returns an error — problems are recorded inside FileDiagnostic.ProbeError.
func ProbeFile(ctx context.Context, path string) FileDiagnostic {
	initTools()
	d := FileDiagnostic{ToolsUsed: []string{}}

	if hasFileCMD {
		runFileCMD(ctx, path, &d)
	}
	if hasFFProbe {
		runFFProbe(ctx, path, &d)
	}
	if hasMediaInfo {
		runMediaInfo(ctx, path, &d)
	}

	deriveFlags(&d)
	return d
}

// ─── tool runners ────────────────────────────────────────────────────────────

func runFileCMD(ctx context.Context, path string, d *FileDiagnostic) {
	ctx2, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx2, "file", "-b", path).Output()
	if err != nil {
		d.ProbeError = appendErr(d.ProbeError, "file: "+err.Error())
		return
	}
	d.ToolsUsed = append(d.ToolsUsed, "file")
	magic := strings.TrimSpace(string(out))
	d.FileMagic = truncate(magic, 200)
	d.IsEmpty = strings.EqualFold(magic, "empty") || strings.Contains(strings.ToLower(magic), "empty")
}

func runFFProbe(ctx context.Context, path string, d *FileDiagnostic) {
	ctx2, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	// First pass: streams + format for codec/duration/bitrate
	args := []string{"-v", "quiet", "-print_format", "json", "-show_streams", "-show_format", "-show_error", path}
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx2, "ffprobe", args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	_ = cmd.Run() // ignore exit code — corrupt files exit non-zero but still emit JSON

	d.ToolsUsed = append(d.ToolsUsed, "ffprobe")

	var ffout struct {
		Format struct {
			FormatName string `json:"format_name"`
			Duration   string `json:"duration"`
			BitRate    string `json:"bit_rate"`
		} `json:"format"`
		Streams []struct {
			CodecName  string `json:"codec_name"`
			CodecType  string `json:"codec_type"`
			BitRate    string `json:"bit_rate"`
			SampleRate string `json:"sample_rate"`
			Channels   int    `json:"channels"`
		} `json:"streams"`
		Error struct {
			Code   int    `json:"code"`
			String string `json:"string"`
		} `json:"error"`
	}
	if raw := limitBytes(stdout.Bytes(), maxOutputBytes); json.Unmarshal(raw, &ffout) == nil {
		d.ContainerFormat = ffout.Format.FormatName
		if ffout.Format.Duration != "" {
			d.DurationSec, _ = strconv.ParseFloat(ffout.Format.Duration, 64)
		}
		if ffout.Format.BitRate != "" {
			if bps, err := strconv.ParseInt(ffout.Format.BitRate, 10, 64); err == nil {
				d.BitrateKbps = int(bps / 1000)
			}
		}
		for _, s := range ffout.Streams {
			if s.CodecType == "audio" {
				d.Codec = s.CodecName
				if s.SampleRate != "" {
					d.SampleRateHz, _ = strconv.Atoi(s.SampleRate)
				}
				if s.Channels > 0 {
					d.Channels = s.Channels
				}
				if s.BitRate != "" && d.BitrateKbps == 0 {
					if bps, err := strconv.ParseInt(s.BitRate, 10, 64); err == nil {
						d.BitrateKbps = int(bps / 1000)
					}
				}
				break
			}
		}
		if ffout.Error.String != "" {
			d.FFProbeErrorStr = truncate(ffout.Error.String, detailMaxBytes)
			d.FFProbeErrorCode = ffout.Error.Code
		}
	}

	// Capture stderr errors (e.g. "moov atom not found", DRM channel errors)
	if stderrStr := strings.TrimSpace(stderr.String()); stderrStr != "" && d.FFProbeErrorStr == "" {
		d.FFProbeErrorStr = truncate(stderrStr, detailMaxBytes)
	}
}

func runMediaInfo(ctx context.Context, path string, d *FileDiagnostic) {
	ctx2, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx2, "mediainfo", "--Output=JSON", path).Output()
	if err != nil {
		d.ProbeError = appendErr(d.ProbeError, "mediainfo: "+err.Error())
		return
	}
	d.ToolsUsed = append(d.ToolsUsed, "mediainfo")

	var mi struct {
		Media struct {
			Track []map[string]string `json:"track"`
		} `json:"media"`
	}
	if json.Unmarshal(limitBytes(out, maxOutputBytes*4), &mi) != nil {
		return
	}
	for _, t := range mi.Media.Track {
		typ := t["@type"]
		switch typ {
		case "General":
			d.MediaInfoFormat = t["Format"]
			d.MediaInfoFormatProfile = t["Format_Profile"]
			d.EncodedApplication = t["Encoded_Application"]
			d.EncodedLibrary = t["Encoded_Library"]
			d.IsStreamable = strings.EqualFold(t["IsStreamable"], "Yes")
			d.Encryption = t["Encryption"]
			if t["Cover"] != "" {
				d.HasCoverArt = strings.EqualFold(t["Cover"], "Yes") || t["Cover"] == "1"
			}
			if v, err := strconv.ParseInt(t["HeaderSize"], 10, 64); err == nil {
				d.HeaderSizeBytes = v
			}
			if v, err := strconv.ParseInt(t["DataSize"], 10, 64); err == nil {
				d.DataSizeBytes = v
			}
			if pos, err := strconv.Atoi(t["Track_Position"]); err == nil {
				d.TrackPosition = pos
			}
			if tot, err := strconv.Atoi(t["Track_Position_Total"]); err == nil {
				d.TrackTotal = tot
			}
			// Prefer mediainfo duration if ffprobe missed it
			if d.DurationSec == 0 && t["Duration"] != "" {
				d.DurationSec, _ = strconv.ParseFloat(t["Duration"], 64)
			}
		}
	}
}

// ─── derivation ──────────────────────────────────────────────────────────────

func deriveFlags(d *FileDiagnostic) {
	// Truncated / incomplete download: moov atom not found
	errLow := strings.ToLower(d.FFProbeErrorStr)
	if strings.Contains(errLow, "moov atom not found") ||
		strings.Contains(errLow, "end of file") {
		d.IsTruncated = true
	}
	// Active DRM: mediainfo Encryption field or Audible DRM channel error
	if d.Encryption != "" && !strings.EqualFold(d.Encryption, "no") {
		d.HasActiveDRM = true
	}
	if strings.Contains(errLow, "channel element") && strings.Contains(errLow, "not allocated") {
		d.HasActiveDRM = true
	}
	// Originally DRM: ripped via inAudible / DeDRM / Requiem
	appLow := strings.ToLower(d.EncodedApplication)
	if strings.Contains(appLow, "inaudible") ||
		strings.Contains(appLow, "dedrm") ||
		strings.Contains(appLow, "requiem") {
		d.WasOriginallyDRM = true
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func limitBytes(b []byte, n int) []byte {
	if len(b) <= n {
		return b
	}
	return b[:n]
}

func appendErr(existing, add string) string {
	if existing == "" {
		return add
	}
	return existing + "; " + add
}
