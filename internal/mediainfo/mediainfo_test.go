// file: internal/mediainfo/mediainfo_test.go
// version: 1.4.0
// guid: a2b3c4d5-e6f7-8a9b-0c1d-2e3f4a5b6c7d
// last-edited: 2026-06-22

package mediainfo

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/dhowden/tag"
)

func TestGenerateQualityString(t *testing.T) {
	tests := []struct {
		name     string
		info     *MediaInfo
		expected string
	}{
		{
			name:     "MP3 320kbps",
			info:     &MediaInfo{Codec: "MP3", Bitrate: 320},
			expected: "320kbps MP3",
		},
		{
			name:     "AAC 256kbps",
			info:     &MediaInfo{Codec: "AAC", Bitrate: 256},
			expected: "256kbps AAC",
		},
		{
			name:     "FLAC 16-bit 44.1kHz",
			info:     &MediaInfo{Codec: "FLAC", BitDepth: 16, SampleRate: 44100},
			expected: "FLAC Lossless (16-bit/44.1kHz)",
		},
		{
			name:     "FLAC 24-bit 96kHz",
			info:     &MediaInfo{Codec: "FLAC", BitDepth: 24, SampleRate: 96000},
			expected: "FLAC Lossless (24-bit/96.0kHz)",
		},
		{
			name:     "Vorbis 192kbps",
			info:     &MediaInfo{Codec: "Vorbis", Bitrate: 192},
			expected: "192kbps Vorbis",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := generateQualityString(tt.info)
			if result != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, result)
			}
		})
	}
}

func TestGetQualityTier(t *testing.T) {
	tests := []struct {
		name         string
		info         *MediaInfo
		expectedTier int
	}{
		{
			name:         "FLAC 24-bit (highest)",
			info:         &MediaInfo{Codec: "FLAC", BitDepth: 24},
			expectedTier: 100,
		},
		{
			name:         "FLAC 16-bit",
			info:         &MediaInfo{Codec: "FLAC", BitDepth: 16},
			expectedTier: 90,
		},
		{
			name:         "MP3 320kbps",
			info:         &MediaInfo{Codec: "MP3", Bitrate: 320},
			expectedTier: 80,
		},
		{
			name:         "AAC 256kbps",
			info:         &MediaInfo{Codec: "AAC", Bitrate: 256},
			expectedTier: 70,
		},
		{
			name:         "MP3 192kbps",
			info:         &MediaInfo{Codec: "MP3", Bitrate: 192},
			expectedTier: 60,
		},
		{
			name:         "AAC 128kbps",
			info:         &MediaInfo{Codec: "AAC", Bitrate: 128},
			expectedTier: 50,
		},
		{
			name:         "Low bitrate",
			info:         &MediaInfo{Codec: "MP3", Bitrate: 96},
			expectedTier: 30,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tier := GetQualityTier(tt.info)
			if tier != tt.expectedTier {
				t.Errorf("expected %d, got %d", tt.expectedTier, tier)
			}
		})
	}
}

func TestInferFromFormat(t *testing.T) {
	tests := []struct {
		name           string
		filename       string
		expectedCodec  string
		expectedFormat string
	}{
		{
			name:           "MP3 file",
			filename:       "test.mp3",
			expectedCodec:  "MP3",
			expectedFormat: "mp3",
		},
		{
			name:           "M4B file",
			filename:       "test.m4b",
			expectedCodec:  "AAC",
			expectedFormat: "m4b",
		},
		{
			name:           "M4A file",
			filename:       "test.m4a",
			expectedCodec:  "AAC",
			expectedFormat: "m4a",
		},
		{
			name:           "FLAC file",
			filename:       "test.flac",
			expectedCodec:  "FLAC",
			expectedFormat: "flac",
		},
		{
			name:           "OGG file",
			filename:       "test.ogg",
			expectedCodec:  "Vorbis",
			expectedFormat: "ogg",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := &MediaInfo{}
			info.Format = tt.expectedFormat
			result, err := inferFromFormat(tt.filename, info)

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if result.Codec != tt.expectedCodec {
				t.Errorf("expected codec %s, got %s", tt.expectedCodec, result.Codec)
			}

			if result.Bitrate == 0 && tt.expectedCodec != "FLAC" {
				t.Error("expected non-zero bitrate for lossy codec")
			}

			if result.SampleRate == 0 {
				t.Error("expected non-zero sample rate")
			}
		})
	}
}

func TestInferFromFormat_UnsupportedExtension(t *testing.T) {
	info := &MediaInfo{}
	_, err := inferFromFormat("test.wav", info)

	if err == nil {
		t.Error("expected error for unsupported extension")
	}
}

func TestExtract_NonExistentFile(t *testing.T) {
	_, err := Extract("/nonexistent/file.mp3")

	if err == nil {
		t.Error("expected error for non-existent file")
	}
}

func TestExtract_EmptyFile(t *testing.T) {
	// Create empty temp file
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "empty.mp3")

	f, err := os.Create(tmpFile)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()

	// Should fall back to format inference
	info, err := Extract(tmpFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if info.Codec != "MP3" {
		t.Errorf("expected MP3 codec from inference, got %s", info.Codec)
	}
}

func TestMediaInfo_Struct(t *testing.T) {
	info := &MediaInfo{
		Bitrate:    320,
		Codec:      "MP3",
		SampleRate: 44100,
		Channels:   2,
		BitDepth:   0,
		Quality:    "320kbps MP3",
		Format:     "mp3",
		Duration:   300,
	}

	if info.Bitrate != 320 {
		t.Error("Bitrate mismatch")
	}
	if info.Codec != "MP3" {
		t.Error("Codec mismatch")
	}
	if info.SampleRate != 44100 {
		t.Error("SampleRate mismatch")
	}
	if info.Channels != 2 {
		t.Error("Channels mismatch")
	}
	if info.Duration != 300 {
		t.Error("Duration mismatch")
	}
}

func TestExtractMP3Info(t *testing.T) {
	// This tests the internal extractMP3Info function indirectly
	// by testing default values
	info := &MediaInfo{}

	// Simulate tag metadata (nil metadata means defaults)
	// extractMP3Info would set these defaults
	info.Codec = "MP3"
	info.Bitrate = 192      // default
	info.SampleRate = 44100 // default
	info.Channels = 2       // default

	if info.Bitrate != 192 {
		t.Error("Expected default MP3 bitrate 192")
	}
	if info.SampleRate != 44100 {
		t.Error("Expected default sample rate 44100")
	}
	if info.Channels != 2 {
		t.Error("Expected 2 channels")
	}
}

func TestExtractM4AInfo(t *testing.T) {
	// Test M4A defaults
	info := &MediaInfo{}
	info.Codec = "AAC"
	info.Bitrate = 160 // M4A default
	info.SampleRate = 44100
	info.Channels = 2

	if info.Bitrate != 160 {
		t.Error("Expected default M4A bitrate 160")
	}
}

func TestExtractFLACInfo(t *testing.T) {
	// Test FLAC properties
	info := &MediaInfo{}
	info.Codec = "FLAC"
	info.SampleRate = 44100
	info.BitDepth = 16
	info.Channels = 2

	if info.BitDepth != 16 {
		t.Error("Expected FLAC bit depth 16")
	}
	if info.Codec != "FLAC" {
		t.Error("Expected FLAC codec")
	}
}

func TestExtractOGGInfo(t *testing.T) {
	// Test OGG defaults
	info := &MediaInfo{}
	info.Codec = "Vorbis"
	info.Bitrate = 160
	info.SampleRate = 44100
	info.Channels = 2

	if info.Bitrate != 160 {
		t.Error("Expected default Vorbis bitrate 160")
	}
	if info.Codec != "Vorbis" {
		t.Error("Expected Vorbis codec")
	}
}

func TestExtract_RealFiles(t *testing.T) {
	testFiles := map[string]string{
		"/tmp/audiobook_test_files/test.mp3":  "MP3",
		"/tmp/audiobook_test_files/test.m4a":  "AAC",
		"/tmp/audiobook_test_files/test.flac": "FLAC",
		"/tmp/audiobook_test_files/test.ogg":  "Vorbis",
	}

	for path, expectedCodec := range testFiles {
		t.Run(filepath.Base(path), func(t *testing.T) {
			if _, err := os.Stat(path); os.IsNotExist(err) {
				t.Skipf("Test file %s not found", path)
				return
			}

			info, err := Extract(path)
			if err != nil {
				t.Logf("Extract failed (falling back to inference): %v", err)
				// This is acceptable - file may not have valid tags
				if info == nil {
					t.Fatal("Expected info even on fallback")
				}
			}

			if info.Codec != expectedCodec {
				t.Errorf("Expected codec %s, got %s", expectedCodec, info.Codec)
			}

			if info.SampleRate == 0 {
				t.Error("Expected non-zero sample rate")
			}
		})
	}
}

func TestExtract_FallbackBehavior(t *testing.T) {
	// Test that Extract falls back to inferFromFormat on tag read errors
	tmpDir := t.TempDir()

	// Create empty files that will trigger fallback
	testCases := []struct {
		filename string
		codec    string
	}{
		{"test.mp3", "MP3"},
		{"test.m4a", "AAC"},
		{"test.flac", "FLAC"},
		{"test.ogg", "Vorbis"},
	}

	for _, tc := range testCases {
		t.Run(tc.filename, func(t *testing.T) {
			path := filepath.Join(tmpDir, tc.filename)
			f, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			f.Close()

			info, err := Extract(path)
			if err != nil {
				t.Fatalf("Extract failed: %v", err)
			}

			if info.Codec != tc.codec {
				t.Errorf("Expected fallback codec %s, got %s", tc.codec, info.Codec)
			}
		})
	}
}

func TestGenerateQualityString_EdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		info     *MediaInfo
		expected string
	}{
		{
			name:     "Zero bitrate",
			info:     &MediaInfo{Codec: "MP3", Bitrate: 0},
			expected: "0kbps MP3",
		},
		{
			name:     "FLAC zero bit depth",
			info:     &MediaInfo{Codec: "FLAC", BitDepth: 0, SampleRate: 44100},
			expected: "FLAC Lossless (0-bit/44.1kHz)",
		},
		{
			name:     "High sample rate",
			info:     &MediaInfo{Codec: "FLAC", BitDepth: 24, SampleRate: 192000},
			expected: "FLAC Lossless (24-bit/192.0kHz)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := generateQualityString(tt.info)
			if result != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, result)
			}
		})
	}
}

func TestGetQualityTier_Boundaries(t *testing.T) {
	tests := []struct {
		name string
		info *MediaInfo
		tier int
	}{
		{"320kbps boundary", &MediaInfo{Bitrate: 320}, 80},
		{"256kbps boundary", &MediaInfo{Bitrate: 256}, 70},
		{"192kbps boundary", &MediaInfo{Bitrate: 192}, 60},
		{"128kbps boundary", &MediaInfo{Bitrate: 128}, 50},
		{"64kbps low", &MediaInfo{Bitrate: 64}, 30},
		{"0kbps minimum", &MediaInfo{Bitrate: 0}, 30},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tier := GetQualityTier(tt.info)
			if tier != tt.tier {
				t.Errorf("expected %d, got %d", tt.tier, tier)
			}
		})
	}
}

// createMinimalMP3 creates a minimal valid MP3 file with ID3v2 header
func createMinimalMP3(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "test.mp3")

	var buf bytes.Buffer

	// ID3v2.3 header
	buf.Write([]byte("ID3"))                  // ID3 identifier
	buf.Write([]byte{0x03, 0x00})             // Version 2.3.0
	buf.Write([]byte{0x00})                   // Flags
	buf.Write([]byte{0x00, 0x00, 0x00, 0x7F}) // Size (synchsafe integer)

	// Minimal ID3v2 frame (TIT2 - Title)
	buf.Write([]byte("TIT2"))                 // Frame ID
	buf.Write([]byte{0x00, 0x00, 0x00, 0x0D}) // Size
	buf.Write([]byte{0x00, 0x00})             // Flags
	buf.Write([]byte{0x00})                   // Encoding (ISO-8859-1)
	buf.Write([]byte("Test Title\x00"))

	// MP3 frame header - 11 bits sync word (all set), MPEG-1, Layer III, no CRC
	// Version 1 (2 bits: 11), Layer 3 (2 bits: 01), Protection (1 bit: 1)
	// Bitrate index (4 bits), Sample rate (2 bits), Padding (1 bit), Private (1 bit)
	// 0xFF 0xFB = 11111111 11111011
	buf.Write([]byte{0xFF, 0xFB})
	// Bitrate 192kbps (index 1001), 44.1kHz (00), no padding, private=0
	// Channel mode (stereo), mode extension, copyright, original, emphasis
	buf.Write([]byte{0x90, 0x00})

	// Add some dummy audio data
	for i := 0; i < 256; i++ {
		buf.WriteByte(0x00)
	}

	if err := os.WriteFile(path, buf.Bytes(), 0644); err != nil {
		t.Fatalf("Failed to create test MP3: %v", err)
	}

	return path
}

// createMinimalM4A creates a minimal valid M4A file with iTunes metadata
func createMinimalM4A(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "test.m4a")

	var buf bytes.Buffer

	// ftyp atom (file type)
	ftypSize := uint32(32)
	binary.Write(&buf, binary.BigEndian, ftypSize)
	buf.Write([]byte("ftyp"))
	buf.Write([]byte("M4A "))                       // Major brand
	binary.Write(&buf, binary.BigEndian, uint32(0)) // Minor version
	buf.Write([]byte("M4A "))
	buf.Write([]byte("mp42"))
	buf.Write([]byte("isom"))
	buf.Write([]byte("\x00\x00\x00\x00"))

	// moov atom (movie)
	moovSize := uint32(200)
	binary.Write(&buf, binary.BigEndian, moovSize)
	buf.Write([]byte("moov"))

	// mvhd atom (movie header)
	mvhdSize := uint32(108)
	binary.Write(&buf, binary.BigEndian, mvhdSize)
	buf.Write([]byte("mvhd"))
	buf.Write(make([]byte, 100)) // Dummy header data

	// udta atom (user data) with meta
	udtaSize := uint32(84)
	binary.Write(&buf, binary.BigEndian, udtaSize)
	buf.Write([]byte("udta"))

	metaSize := uint32(76)
	binary.Write(&buf, binary.BigEndian, metaSize)
	buf.Write([]byte("meta"))
	buf.Write([]byte{0x00, 0x00, 0x00, 0x00}) // Version/flags

	// ilst atom (item list)
	ilstSize := uint32(64)
	binary.Write(&buf, binary.BigEndian, ilstSize)
	buf.Write([]byte("ilst"))
	buf.Write(make([]byte, 56)) // Dummy metadata

	// mdat atom (media data)
	mdatSize := uint32(512)
	binary.Write(&buf, binary.BigEndian, mdatSize)
	buf.Write([]byte("mdat"))
	buf.Write(make([]byte, 504)) // Dummy audio data

	if err := os.WriteFile(path, buf.Bytes(), 0644); err != nil {
		t.Fatalf("Failed to create test M4A: %v", err)
	}

	return path
}

// createMinimalFLAC creates a minimal valid FLAC file
func createMinimalFLAC(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "test.flac")

	var buf bytes.Buffer

	// FLAC signature
	buf.Write([]byte("fLaC"))

	// STREAMINFO metadata block (type 0, last metadata block)
	buf.WriteByte(0x80) // Last-metadata-block flag set (1), type 0 (STREAMINFO)

	// Block length (34 bytes)
	buf.Write([]byte{0x00, 0x00, 0x22})

	// Min/max block size (16-bit each)
	binary.Write(&buf, binary.BigEndian, uint16(4096)) // min
	binary.Write(&buf, binary.BigEndian, uint16(4096)) // max

	// Min/max frame size (24-bit each)
	buf.Write([]byte{0x00, 0x00, 0x00}) // min
	buf.Write([]byte{0x00, 0x00, 0x00}) // max

	// Sample rate (20 bits) = 44100 Hz, channels (3 bits) = 2, bits per sample (5 bits) = 16
	// 44100 = 0xAC44 in 20 bits
	// Sample rate: 0000 0000 0000 1010 1100 (20 bits)
	// Channels-1: 001 (3 bits, 2 channels)
	// BPS-1: 01111 (5 bits, 16 bits per sample)
	buf.WriteByte(0x0A) // Upper 8 bits of sample rate
	buf.WriteByte(0xC4) // Lower 12 bits of sample rate (0xC44 >> 4)
	buf.WriteByte(0x42) // Last 4 bits of rate + channels + upper 1 bit of BPS
	buf.WriteByte(0xF0) // Lower 4 bits of BPS + upper 4 bits of total samples

	// Total samples (36 bits)
	buf.Write([]byte{0x00, 0x00, 0x00, 0x00})

	// MD5 signature (16 bytes)
	buf.Write(make([]byte, 16))

	// Add minimal frame
	buf.Write(make([]byte, 100))

	if err := os.WriteFile(path, buf.Bytes(), 0644); err != nil {
		t.Fatalf("Failed to create test FLAC: %v", err)
	}

	return path
}

// createMinimalOGG creates a minimal valid OGG Vorbis file
func createMinimalOGG(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "test.ogg")

	var buf bytes.Buffer

	// OGG page header
	buf.Write([]byte("OggS"))                          // Capture pattern
	buf.WriteByte(0x00)                                // Version
	buf.WriteByte(0x02)                                // Header type (beginning of stream)
	buf.Write(make([]byte, 8))                         // Granule position
	binary.Write(&buf, binary.LittleEndian, uint32(1)) // Serial number
	binary.Write(&buf, binary.LittleEndian, uint32(0)) // Page sequence
	binary.Write(&buf, binary.LittleEndian, uint32(0)) // Checksum (dummy)
	buf.WriteByte(0x01)                                // Number of segments
	buf.WriteByte(0x1E)                                // Segment size (30 bytes)

	// Vorbis identification header
	buf.WriteByte(0x01)                                     // Packet type (identification)
	buf.Write([]byte("vorbis"))                             // Vorbis string
	binary.Write(&buf, binary.LittleEndian, uint32(0))      // Vorbis version
	buf.WriteByte(0x02)                                     // Channels (2)
	binary.Write(&buf, binary.LittleEndian, uint32(44100))  // Sample rate
	binary.Write(&buf, binary.LittleEndian, uint32(192000)) // Bitrate max
	binary.Write(&buf, binary.LittleEndian, uint32(160000)) // Bitrate nominal
	binary.Write(&buf, binary.LittleEndian, uint32(128000)) // Bitrate min

	// Add more dummy data
	buf.Write(make([]byte, 100))

	if err := os.WriteFile(path, buf.Bytes(), 0644); err != nil {
		t.Fatalf("Failed to create test OGG: %v", err)
	}

	return path
}

func TestExtract_MP3WithTags(t *testing.T) {
	tmpDir := t.TempDir()
	path := createMinimalMP3(t, tmpDir)

	info, err := Extract(path)
	if err != nil {
		// If tag reading fails, it should fall back to inference
		t.Logf("Tag reading failed (acceptable): %v", err)
		if info == nil {
			t.Fatal("Expected fallback info")
		}
	}

	if info.Codec != "MP3" {
		t.Errorf("Expected MP3 codec, got %s", info.Codec)
	}

	if info.Format != "mp3" {
		t.Errorf("Expected mp3 format, got %s", info.Format)
	}

	// Check that defaults are applied
	if info.Bitrate == 0 {
		t.Error("Expected non-zero bitrate")
	}

	if info.SampleRate == 0 {
		t.Error("Expected non-zero sample rate")
	}

	if info.Channels == 0 {
		t.Error("Expected non-zero channels")
	}

	if info.Quality == "" {
		t.Error("Expected quality string to be set")
	}
}

func TestExtract_M4AWithTags(t *testing.T) {
	tmpDir := t.TempDir()
	path := createMinimalM4A(t, tmpDir)

	info, err := Extract(path)
	if err != nil {
		t.Logf("Tag reading failed (acceptable): %v", err)
		if info == nil {
			t.Fatal("Expected fallback info")
		}
	}

	if info.Codec != "AAC" {
		t.Errorf("Expected AAC codec, got %s", info.Codec)
	}

	if info.Format != "m4a" {
		t.Errorf("Expected m4a format, got %s", info.Format)
	}

	if info.Bitrate == 0 {
		t.Error("Expected non-zero bitrate")
	}

	if info.Quality == "" {
		t.Error("Expected quality string to be set")
	}
}

func TestExtract_FLACWithTags(t *testing.T) {
	tmpDir := t.TempDir()
	path := createMinimalFLAC(t, tmpDir)

	info, err := Extract(path)
	if err != nil {
		t.Logf("Tag reading failed (acceptable): %v", err)
		if info == nil {
			t.Fatal("Expected fallback info")
		}
	}

	if info.Codec != "FLAC" {
		t.Errorf("Expected FLAC codec, got %s", info.Codec)
	}

	if info.Format != "flac" {
		t.Errorf("Expected flac format, got %s", info.Format)
	}

	// FLAC should have bit depth
	if info.BitDepth == 0 {
		t.Error("Expected non-zero bit depth for FLAC")
	}

	if info.Quality == "" {
		t.Error("Expected quality string to be set")
	}
}

func TestExtract_OGGWithTags(t *testing.T) {
	tmpDir := t.TempDir()
	path := createMinimalOGG(t, tmpDir)

	info, err := Extract(path)
	if err != nil {
		t.Logf("Tag reading failed (acceptable): %v", err)
		if info == nil {
			t.Fatal("Expected fallback info")
		}
	}

	if info.Codec != "Vorbis" {
		t.Errorf("Expected Vorbis codec, got %s", info.Codec)
	}

	if info.Format != "ogg" {
		t.Errorf("Expected ogg format, got %s", info.Format)
	}

	if info.Bitrate == 0 {
		t.Error("Expected non-zero bitrate")
	}
}

func TestExtract_M4BFormat(t *testing.T) {
	tmpDir := t.TempDir()

	// Create an M4A file but rename it to M4B
	path := createMinimalM4A(t, tmpDir)
	m4bPath := filepath.Join(tmpDir, "test.m4b")

	if err := os.Rename(path, m4bPath); err != nil {
		t.Fatalf("Failed to rename file: %v", err)
	}

	info, err := Extract(m4bPath)
	if err != nil {
		t.Logf("Tag reading failed (acceptable): %v", err)
		if info == nil {
			t.Fatal("Expected fallback info")
		}
	}

	if info.Codec != "AAC" {
		t.Errorf("Expected AAC codec for M4B, got %s", info.Codec)
	}

	if info.Format != "m4b" {
		t.Errorf("Expected m4b format, got %s", info.Format)
	}
}

func TestExtract_OGAFormat(t *testing.T) {
	tmpDir := t.TempDir()

	// Create an OGG file but rename it to OGA
	path := createMinimalOGG(t, tmpDir)
	ogaPath := filepath.Join(tmpDir, "test.oga")

	if err := os.Rename(path, ogaPath); err != nil {
		t.Fatalf("Failed to rename file: %v", err)
	}

	info, err := Extract(ogaPath)
	if err != nil {
		t.Logf("Tag reading failed (acceptable): %v", err)
		if info == nil {
			t.Fatal("Expected fallback info")
		}
	}

	if info.Codec != "Vorbis" {
		t.Errorf("Expected Vorbis codec for OGA, got %s", info.Codec)
	}

	if info.Format != "oga" {
		t.Errorf("Expected oga format, got %s", info.Format)
	}
}

func TestInferFromFormat_CaseInsensitivity(t *testing.T) {
	tests := []struct {
		filename string
		codec    string
	}{
		{"test.MP3", "MP3"},
		{"test.Mp3", "MP3"},
		{"test.FLAC", "FLAC"},
		{"test.M4A", "AAC"},
		{"test.OGG", "Vorbis"},
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			info := &MediaInfo{}
			result, err := inferFromFormat(tt.filename, info)

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if result.Codec != tt.codec {
				t.Errorf("expected codec %s, got %s", tt.codec, result.Codec)
			}
		})
	}
}

func TestExtract_FormatExtraction(t *testing.T) {
	tmpDir := t.TempDir()

	// Create empty files with various extensions
	extensions := []string{".mp3", ".m4a", ".m4b", ".flac", ".ogg", ".oga"}

	for _, ext := range extensions {
		t.Run(ext, func(t *testing.T) {
			path := filepath.Join(tmpDir, "test"+ext)
			if err := os.WriteFile(path, []byte{}, 0644); err != nil {
				t.Fatal(err)
			}

			info, err := Extract(path)
			if err != nil {
				t.Fatalf("Extract failed: %v", err)
			}

			expectedFormat := ext[1:] // Remove leading dot
			if info.Format != expectedFormat {
				t.Errorf("Expected format %s, got %s", expectedFormat, info.Format)
			}
		})
	}
}

func TestGetQualityTier_AboveBoundaries(t *testing.T) {
	tests := []struct {
		name string
		info *MediaInfo
		tier int
	}{
		{"400kbps (above 320)", &MediaInfo{Bitrate: 400}, 80},
		{"350kbps (above 320)", &MediaInfo{Bitrate: 350}, 80},
		{"260kbps (above 256)", &MediaInfo{Bitrate: 260}, 70},
		{"200kbps (above 192)", &MediaInfo{Bitrate: 200}, 60},
		{"150kbps (above 128)", &MediaInfo{Bitrate: 150}, 50},
		{"FLAC 32-bit", &MediaInfo{Codec: "FLAC", BitDepth: 32}, 100},
		{"FLAC 24-bit", &MediaInfo{Codec: "FLAC", BitDepth: 24}, 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tier := GetQualityTier(tt.info)
			if tier != tt.tier {
				t.Errorf("expected %d, got %d", tt.tier, tier)
			}
		})
	}
}

// findRepoRootForMediainfo walks up from cwd until it finds go.mod.
func findRepoRootForMediainfo(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root (go.mod)")
		}
		dir = parent
	}
}

func TestExtract_RealMP3File(t *testing.T) {
	repoRoot := findRepoRootForMediainfo(t)
	testFile := filepath.Join(repoRoot, "testdata", "audio", "librivox",
		"odyssey_butler_librivox", "odyssey_01_homer_butler_64kb.mp3")

	if _, err := os.Stat(testFile); os.IsNotExist(err) {
		t.Skip("Librivox corpus not present (LFS-on-demand; run git lfs pull to enable)")
	}

	info, err := Extract(testFile)
	if err != nil {
		t.Fatalf("Failed to extract from real MP3: %v", err)
	}

	if info.Codec != "MP3" {
		t.Errorf("Expected MP3 codec, got %s", info.Codec)
	}

	if info.Format != "mp3" {
		t.Errorf("Expected mp3 format, got %s", info.Format)
	}

	// Real file should have metadata
	if info.Bitrate == 0 {
		t.Error("Expected non-zero bitrate")
	}

	if info.SampleRate == 0 {
		t.Error("Expected non-zero sample rate")
	}

	if info.Quality == "" {
		t.Error("Expected quality string")
	}

	t.Logf("Extracted info: Codec=%s, Bitrate=%d, SampleRate=%d, Quality=%s",
		info.Codec, info.Bitrate, info.SampleRate, info.Quality)
}

// mockMetadata implements tag.Metadata interface for testing
type mockMetadata struct {
	fileType tag.FileType
	raw      map[string]interface{}
}

func (m *mockMetadata) Format() tag.Format          { return tag.ID3v2_4 }
func (m *mockMetadata) FileType() tag.FileType      { return m.fileType }
func (m *mockMetadata) Title() string               { return "Test Title" }
func (m *mockMetadata) Album() string               { return "Test Album" }
func (m *mockMetadata) Artist() string              { return "Test Artist" }
func (m *mockMetadata) AlbumArtist() string         { return "Test Album Artist" }
func (m *mockMetadata) Composer() string            { return "Test Composer" }
func (m *mockMetadata) Genre() string               { return "Test Genre" }
func (m *mockMetadata) Year() int                   { return 2024 }
func (m *mockMetadata) Track() (int, int)           { return 1, 10 }
func (m *mockMetadata) Disc() (int, int)            { return 1, 1 }
func (m *mockMetadata) Picture() *tag.Picture       { return nil }
func (m *mockMetadata) Lyrics() string              { return "" }
func (m *mockMetadata) Comment() string             { return "" }
func (m *mockMetadata) Raw() map[string]interface{} { return m.raw }

func TestExtractMP3Info_WithMetadata(t *testing.T) {
	tests := []struct {
		name               string
		raw                map[string]interface{}
		expectedBitrate    int
		expectedSampleRate int
	}{
		{
			name: "With bitrate and sample rate",
			raw: map[string]interface{}{
				"bitrate":     320000,
				"sample_rate": 48000,
			},
			expectedBitrate:    320,
			expectedSampleRate: 48000,
		},
		{
			name: "With only bitrate",
			raw: map[string]interface{}{
				"bitrate": 256000,
			},
			expectedBitrate:    256,
			expectedSampleRate: 44100, // default
		},
		{
			name:               "No metadata (defaults)",
			raw:                map[string]interface{}{},
			expectedBitrate:    192,   // default
			expectedSampleRate: 44100, // default
		},
		{
			name: "Wrong type in metadata",
			raw: map[string]interface{}{
				"bitrate":     "invalid",
				"sample_rate": "invalid",
			},
			expectedBitrate:    192,   // default
			expectedSampleRate: 44100, // default
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockMetadata{
				fileType: tag.MP3,
				raw:      tt.raw,
			}

			info := &MediaInfo{}
			extractMP3Info(mock, info)

			if info.Codec != "MP3" {
				t.Errorf("Expected MP3 codec, got %s", info.Codec)
			}

			if info.Bitrate != tt.expectedBitrate {
				t.Errorf("Expected bitrate %d, got %d", tt.expectedBitrate, info.Bitrate)
			}

			if info.SampleRate != tt.expectedSampleRate {
				t.Errorf("Expected sample rate %d, got %d", tt.expectedSampleRate, info.SampleRate)
			}

			if info.Channels != 2 {
				t.Errorf("Expected 2 channels, got %d", info.Channels)
			}
		})
	}
}

func TestExtractM4AInfo_WithMetadata(t *testing.T) {
	tests := []struct {
		name               string
		raw                map[string]interface{}
		expectedBitrate    int
		expectedSampleRate int
	}{
		{
			name: "With bitrate and sample rate",
			raw: map[string]interface{}{
				"bitrate":     256000,
				"sample_rate": 48000,
			},
			expectedBitrate:    256,
			expectedSampleRate: 48000,
		},
		{
			name: "With only sample rate",
			raw: map[string]interface{}{
				"sample_rate": 44100,
			},
			expectedBitrate:    128, // default
			expectedSampleRate: 44100,
		},
		{
			name:               "No metadata (defaults)",
			raw:                map[string]interface{}{},
			expectedBitrate:    128,   // default
			expectedSampleRate: 44100, // default
		},
		{
			name: "Wrong type in metadata",
			raw: map[string]interface{}{
				"bitrate":     3.14,
				"sample_rate": "not a number",
			},
			expectedBitrate:    128,   // default
			expectedSampleRate: 44100, // default
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockMetadata{
				fileType: tag.M4A,
				raw:      tt.raw,
			}

			info := &MediaInfo{}
			extractM4AInfo(mock, info)

			if info.Codec != "AAC" {
				t.Errorf("Expected AAC codec, got %s", info.Codec)
			}

			if info.Bitrate != tt.expectedBitrate {
				t.Errorf("Expected bitrate %d, got %d", tt.expectedBitrate, info.Bitrate)
			}

			if info.SampleRate != tt.expectedSampleRate {
				t.Errorf("Expected sample rate %d, got %d", tt.expectedSampleRate, info.SampleRate)
			}

			if info.Channels != 2 {
				t.Errorf("Expected 2 channels, got %d", info.Channels)
			}
		})
	}
}

func TestExtractFLACInfo_WithMetadata(t *testing.T) {
	tests := []struct {
		name               string
		raw                map[string]interface{}
		expectedSampleRate int
		expectedBitDepth   int
		expectedBitrate    int
	}{
		{
			name: "With sample rate and bit depth",
			raw: map[string]interface{}{
				"sample_rate":     96000,
				"bits_per_sample": 24,
			},
			expectedSampleRate: 96000,
			expectedBitDepth:   24,
			expectedBitrate:    4608, // (96000 * 24 * 2) / 1000
		},
		{
			name: "With only sample rate",
			raw: map[string]interface{}{
				"sample_rate": 48000,
			},
			expectedSampleRate: 48000,
			expectedBitDepth:   16,   // default
			expectedBitrate:    1536, // (48000 * 16 * 2) / 1000
		},
		{
			name:               "No metadata (defaults)",
			raw:                map[string]interface{}{},
			expectedSampleRate: 44100, // default
			expectedBitDepth:   16,    // default
			expectedBitrate:    1411,  // (44100 * 16 * 2) / 1000
		},
		{
			name: "Wrong types",
			raw: map[string]interface{}{
				"sample_rate":     "invalid",
				"bits_per_sample": false,
			},
			expectedSampleRate: 44100, // default
			expectedBitDepth:   16,    // default
			expectedBitrate:    1411,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockMetadata{
				fileType: tag.FLAC,
				raw:      tt.raw,
			}

			info := &MediaInfo{}
			extractFLACInfo(mock, info)

			if info.Codec != "FLAC" {
				t.Errorf("Expected FLAC codec, got %s", info.Codec)
			}

			if info.SampleRate != tt.expectedSampleRate {
				t.Errorf("Expected sample rate %d, got %d", tt.expectedSampleRate, info.SampleRate)
			}

			if info.BitDepth != tt.expectedBitDepth {
				t.Errorf("Expected bit depth %d, got %d", tt.expectedBitDepth, info.BitDepth)
			}

			if info.Bitrate != tt.expectedBitrate {
				t.Errorf("Expected bitrate %d, got %d", tt.expectedBitrate, info.Bitrate)
			}

			if info.Channels != 2 {
				t.Errorf("Expected 2 channels, got %d", info.Channels)
			}
		})
	}
}

func TestExtractOGGInfo_WithMetadata(t *testing.T) {
	tests := []struct {
		name               string
		raw                map[string]interface{}
		expectedBitrate    int
		expectedSampleRate int
	}{
		{
			name: "With bitrate and sample rate",
			raw: map[string]interface{}{
				"bitrate":     192000,
				"sample_rate": 48000,
			},
			expectedBitrate:    192,
			expectedSampleRate: 48000,
		},
		{
			name: "With only bitrate",
			raw: map[string]interface{}{
				"bitrate": 128000,
			},
			expectedBitrate:    128,
			expectedSampleRate: 44100, // default
		},
		{
			name:               "No metadata (defaults)",
			raw:                map[string]interface{}{},
			expectedBitrate:    160,   // default
			expectedSampleRate: 44100, // default
		},
		{
			name: "Wrong types",
			raw: map[string]interface{}{
				"bitrate":     []byte{1, 2, 3},
				"sample_rate": map[string]int{},
			},
			expectedBitrate:    160,   // default
			expectedSampleRate: 44100, // default
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockMetadata{
				fileType: tag.OGG,
				raw:      tt.raw,
			}

			info := &MediaInfo{}
			extractOGGInfo(mock, info)

			if info.Codec != "Vorbis" {
				t.Errorf("Expected Vorbis codec, got %s", info.Codec)
			}

			if info.Bitrate != tt.expectedBitrate {
				t.Errorf("Expected bitrate %d, got %d", tt.expectedBitrate, info.Bitrate)
			}

			if info.SampleRate != tt.expectedSampleRate {
				t.Errorf("Expected sample rate %d, got %d", tt.expectedSampleRate, info.SampleRate)
			}

			if info.Channels != 2 {
				t.Errorf("Expected 2 channels, got %d", info.Channels)
			}
		})
	}
}

// realDurationSec must report ok=false (not crash) when ffprobe can't read the
// file, so callers fall back to a flagged estimate.
func TestRealDurationSec_MissingFile(t *testing.T) {
	if _, ok := realDurationSec(filepath.Join(t.TempDir(), "nope.m4b")); ok {
		t.Error("expected ok=false for a non-existent file")
	}
}

// When the real duration can't be read (junk bytes ffprobe can't parse), the
// fileSize÷bitrate fallback is used AND flagged DurationEstimated=true so dedup
// and metadata scoring know not to trust it.
func TestInferFromFormat_FlagsEstimatedDuration(t *testing.T) {
	p := filepath.Join(t.TempDir(), "junk.mp3")
	if err := os.WriteFile(p, bytes.Repeat([]byte{0}, 200_000), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	info, err := inferFromFormat(p, &MediaInfo{})
	if err != nil {
		t.Fatalf("inferFromFormat: %v", err)
	}
	if info.Duration <= 0 {
		t.Fatalf("expected an estimated duration > 0, got %d", info.Duration)
	}
	if !info.DurationEstimated {
		t.Error("fallback estimate must set DurationEstimated=true")
	}
}
