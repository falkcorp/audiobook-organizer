// file: internal/itunes/itl.go
// version: 1.10.0
// guid: 7f2a8b3c-4d5e-6f01-a2b3-c4d5e6f7a8b9
// last-edited: 2026-10-07

package itunes

import (
	"bytes"
	"compress/zlib"
	"crypto/aes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/text/encoding/charmap"
)

// itlAESKey is the hardcoded AES key used by iTunes for ITL encryption.
var itlAESKey = []byte("BHUILuilfghuila3")

// macEpoch is 1904-01-01 00:00:00 UTC, the Mac HFS+ epoch.
var macEpoch = time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC)

// ITLPlaylist represents a playlist from the ITL binary.
type ITLPlaylist struct {
	PersistentID  [8]byte // 8-byte playlist persistent ID (ppid)
	Title         string  // From hohm type 0x64
	IsFolder      bool    // True if this is a playlist folder (issue #11) — TODO: reliable detection
	IsSmart       bool    // Has smart criteria
	Items         []int   // Track IDs referenced by this playlist (hptm records)
	SmartInfo     []byte  // Raw smart info blob (hohm 0x66)
	SmartCriteria []byte  // Raw smart criteria blob (hohm 0x65)
}

// ITLLibrary represents a parsed iTunes .itl binary library.
type ITLLibrary struct {
	Version         string
	HeaderRemainder []byte
	Tracks          []ITLTrack
	Playlists       []ITLPlaylist
	UseCompression  bool
	rawData         []byte // decrypted (and decompressed) payload
	unknown         uint32 // from hdfm header
}

// RawData returns the decrypted/decompressed payload for debugging.
func (lib *ITLLibrary) RawData() []byte {
	return lib.rawData
}

// ITLTrack represents a track from the ITL binary.
type ITLTrack struct {
	TrackID           int
	PersistentID      [8]byte
	AlbumPersistentID [8]byte
	Name              string
	Album             string
	Artist            string
	Genre             string
	Kind              string
	Location          string // hohm type 0x0D
	LocalURL          string // hohm type 0x0B
	Size              int
	TotalTime         int // milliseconds
	TrackNumber       int
	TrackCount        int
	DiscNumber        int
	DiscCount         int
	Year              int
	BitRate           int
	SampleRate        int
	PlayCount         int
	Rating            int // 0-100
	DateModified      time.Time
	DateAdded         time.Time
	LastPlayDate      time.Time
}

// ITLLocationUpdate maps a persistent ID to a new file location.
type ITLLocationUpdate struct {
	PersistentID string // hex-encoded 8-byte ID
	NewLocation  string
}

// ---------------------------------------------------------------------------
// Helper functions
// ---------------------------------------------------------------------------

func readTag(data []byte, offset int) string {
	if offset+4 > len(data) {
		return ""
	}
	return string(data[offset : offset+4])
}

func readUint32BE(data []byte, offset int) uint32 {
	if offset+4 > len(data) {
		return 0
	}
	return binary.BigEndian.Uint32(data[offset : offset+4])
}

func readUint16BE(data []byte, offset int) uint16 {
	if offset+2 > len(data) {
		return 0
	}
	return binary.BigEndian.Uint16(data[offset : offset+2])
}

func writeUint32BE(buf []byte, offset int, val uint32) {
	if offset+4 <= len(buf) {
		binary.BigEndian.PutUint32(buf[offset:offset+4], val)
	}
}

func readUint32LE(data []byte, offset int) uint32 {
	if offset+4 > len(data) {
		return 0
	}
	return uint32(data[offset]) | uint32(data[offset+1])<<8 |
		uint32(data[offset+2])<<16 | uint32(data[offset+3])<<24
}

func readUint16LE(data []byte, offset int) uint16 {
	if offset+2 > len(data) {
		return 0
	}
	return uint16(data[offset]) | uint16(data[offset+1])<<8
}

func detectLE(data []byte) bool {
	if len(data) < 4 {
		return false
	}
	return string(data[0:4]) == "msdh"
}

func pidToHex(pid [8]byte) string {
	return hex.EncodeToString(pid[:])
}

func hexToPID(h string) ([8]byte, error) {
	var pid [8]byte
	b, err := hex.DecodeString(h)
	if err != nil {
		return pid, fmt.Errorf("invalid hex persistent ID: %w", err)
	}
	if len(b) != 8 {
		return pid, fmt.Errorf("persistent ID must be 8 bytes, got %d", len(b))
	}
	copy(pid[:], b)
	return pid, nil
}

func macDateToTime(seconds uint32) time.Time {
	if seconds == 0 {
		return time.Time{}
	}
	return macEpoch.Add(time.Duration(seconds) * time.Second)
}

func isVersionAtLeast(version string, major int) bool {
	if version == "" {
		return false
	}
	parts := strings.SplitN(version, ".", 2)
	v, err := strconv.Atoi(parts[0])
	if err != nil {
		return false
	}
	return v >= major
}

// ---------------------------------------------------------------------------
// AES-128/ECB encrypt/decrypt
// ---------------------------------------------------------------------------

func itlDecrypt(hdr *hdfmHeader, data []byte) []byte {
	if len(data) == 0 {
		return data
	}
	block, err := aes.NewCipher(itlAESKey)
	if err != nil {
		return data
	}
	bs := block.BlockSize()

	limit := len(data)
	if isVersionAtLeast(hdr.version, 10) {
		if hdr.maxCryptSize > 0 {
			limit = int(hdr.maxCryptSize)
		} else if limit > 102400 {
			limit = 102400
		}
	}
	if limit > len(data) {
		limit = len(data)
	}
	limit = (limit / bs) * bs

	out := make([]byte, len(data))
	copy(out, data)

	for i := 0; i < limit; i += bs {
		block.Decrypt(out[i:i+bs], data[i:i+bs])
	}
	return out
}

// ---------------------------------------------------------------------------
// Zlib compression
// ---------------------------------------------------------------------------

// maxDecompressedSize caps zlib decompression to prevent decompression bombs.
// 2 GB: legitimate iTunes library payloads are ~236MB and growing;
// fail closed on bomb or exceed, never silently pass through.
const maxDecompressedSize = 2 * 1024 * 1024 * 1024

// itlInflate decompresses a zlib-compressed ITL payload.
// Returns (data, wasCompressed, error).
// If payload doesn't start with 0x78 (zlib magic byte), returns (data, false, nil) —
// legitimately uncompressed payload.
// If zlib decompression fails or exceeds maxDecompressedSize, returns (nil, false, error) —
// fail-closed behavior required because downstream verifiers fail open on parse errors.
func itlInflate(data []byte) ([]byte, bool, error) {
	if len(data) == 0 || data[0] != 0x78 {
		// Not a zlib stream: pass through as-is, uncompressed
		return data, false, nil
	}
	r, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		// zlib stream expected but reader failed: explicit error, never silent fallback
		return nil, false, fmt.Errorf("zlib decompression: %w", err)
	}
	defer r.Close()
	limited := io.LimitReader(r, maxDecompressedSize+1)
	out, err := io.ReadAll(limited)
	if err != nil {
		// decompression read failed: explicit error
		return nil, false, fmt.Errorf("zlib decompression read: %w", err)
	}
	if int64(len(out)) > maxDecompressedSize {
		// decompressed data exceeds size limit — explicit error, reject as decompression bomb
		return nil, false, fmt.Errorf("decompressed ITL payload %d bytes exceeds cap %d bytes (decompression bomb)", len(out), maxDecompressedSize)
	}
	return out, true, nil
}

// ---------------------------------------------------------------------------
// String encoding/decoding for hohm records
// ---------------------------------------------------------------------------

// decodeHohmString decodes a string from hohm payload data.
// encodingFlag: 0=ASCII, 1=UTF-16BE, 2=UTF-8, 3=Windows-1252
func decodeHohmString(data []byte, encodingFlag byte) (string, error) {
	switch encodingFlag {
	case 0: // ASCII
		return string(data), nil
	case 1: // UTF-16BE
		if len(data)%2 != 0 {
			data = append(data, 0)
		}
		runes := make([]rune, len(data)/2)
		for i := 0; i < len(data)/2; i++ {
			runes[i] = rune(binary.BigEndian.Uint16(data[i*2 : i*2+2]))
		}
		return string(runes), nil
	case 2: // UTF-8
		return string(data), nil
	case 3: // Windows-1252
		dec := charmap.Windows1252.NewDecoder()
		out, err := dec.Bytes(data)
		if err != nil {
			return string(data), err
		}
		return string(out), nil
	default:
		return string(data), fmt.Errorf("unknown hohm encoding flag: %d", encodingFlag)
	}
}

// ---------------------------------------------------------------------------
// hdfm header parsing
// ---------------------------------------------------------------------------

type hdfmHeader struct {
	headerLen       uint32
	fileLen         uint32
	unknown         uint32
	version         string
	headerRemainder []byte
	maxCryptSize    uint32 // from offset 92 in header, controls encryption boundary
}

func parseHdfmHeader(data []byte) (*hdfmHeader, error) {
	if len(data) < 8 {
		return nil, fmt.Errorf("file too small for ITL header")
	}
	tag := readTag(data, 0)
	if tag != "hdfm" {
		return nil, fmt.Errorf("not an ITL file: expected 'hdfm', got %q", tag)
	}
	headerLen := readUint32BE(data, 4)
	if int(headerLen) > len(data) {
		return nil, fmt.Errorf("hdfm header length %d exceeds file size %d", headerLen, len(data))
	}
	if headerLen < 16 {
		return nil, fmt.Errorf("hdfm header too short: %d", headerLen)
	}

	fileLen := readUint32BE(data, 8)
	unknown := readUint32BE(data, 12)

	// Version string length is a single byte (per Java: readUnsignedByte())
	off := 16
	if off+1 > int(headerLen) {
		return nil, fmt.Errorf("hdfm header truncated at version length")
	}
	verLen := int(data[off])
	off++
	if off+verLen > int(headerLen) {
		return nil, fmt.Errorf("hdfm version string exceeds header")
	}
	version := string(data[off : off+verLen])
	off += verLen

	// Read max_crypt_size at absolute offset 92 if header is large enough
	var maxCryptSize uint32
	if headerLen > 96 {
		maxCryptSize = readUint32BE(data, 92)
	}

	var remainder []byte
	const maxITLFieldSize = uint32(256 * 1024 * 1024) // 256 MiB sanity cap
	if headerLen > maxITLFieldSize {
		return nil, fmt.Errorf("hdfm header length %d exceeds max %d", headerLen, maxITLFieldSize)
	}
	if off < int(headerLen) {
		remainder = make([]byte, int(headerLen)-off)
		copy(remainder, data[off:int(headerLen)])
	}

	return &hdfmHeader{
		headerLen:       headerLen,
		fileLen:         fileLen,
		unknown:         unknown,
		version:         version,
		headerRemainder: remainder,
		maxCryptSize:    maxCryptSize,
	}, nil
}

func buildHdfmHeader(version string, remainder []byte, fileLen uint32, unknown uint32) []byte {
	verBytes := []byte(version)
	// Header: "hdfm"(4) + headerLen(4) + fileLen(4) + unknown(4) + verLen(1) + version(N) + remainder
	headerLen := 17 + len(verBytes) + len(remainder)
	buf := make([]byte, headerLen)
	copy(buf[0:4], "hdfm")
	writeUint32BE(buf, 4, uint32(headerLen))
	writeUint32BE(buf, 8, fileLen)
	writeUint32BE(buf, 12, unknown)
	buf[16] = byte(len(verBytes))
	copy(buf[17:17+len(verBytes)], verBytes)
	if len(remainder) > 0 {
		copy(buf[17+len(verBytes):], remainder)
	}
	return buf
}

// ---------------------------------------------------------------------------
// Chunk walking for read path
// ---------------------------------------------------------------------------

// ParseITL reads and parses an iTunes .itl binary library file.
func ParseITL(path string) (*ITLLibrary, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading ITL file: %w", err)
	}
	return parseITLData(data)
}

func parseITLData(data []byte) (*ITLLibrary, error) {
	hdr, err := parseHdfmHeader(data)
	if err != nil {
		return nil, err
	}

	// Decrypt payload (everything after hdfm header)
	payload := data[hdr.headerLen:]
	decrypted := itlDecrypt(hdr, payload)

	// Decompress
	decompressed, wasCompressed, err := itlInflate(decrypted)
	if err != nil {
		return nil, fmt.Errorf("decompressing ITL payload: %w", err)
	}

	lib := &ITLLibrary{
		Version:         hdr.version,
		HeaderRemainder: hdr.headerRemainder,
		UseCompression:  wasCompressed,
		rawData:         decompressed,
		unknown:         hdr.unknown,
	}

	// Walk chunks — dispatch on endianness
	if detectLE(decompressed) {
		walkChunksLE(decompressed, lib)
	} else {
		walkChunksBE(decompressed, lib)
	}

	return lib, nil
}

// walkChunksLE dispatches to the LE implementation in itl_le.go.
func walkChunksLE(data []byte, lib *ITLLibrary) {
	walkChunksLEImpl(data, lib)
}

// ---------------------------------------------------------------------------
// Chunk builders for write path
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// ValidateITL performs a quick validation of an ITL file.
// ---------------------------------------------------------------------------

// ValidateITL checks that a file is a valid ITL by reading and decrypting the header.
func ValidateITL(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading ITL: %w", err)
	}
	if len(data) < 20 {
		return fmt.Errorf("file too small to be ITL: %d bytes", len(data))
	}

	hdr, err := parseHdfmHeader(data)
	if err != nil {
		return err
	}

	payload := data[hdr.headerLen:]
	if len(payload) == 0 {
		return fmt.Errorf("ITL has no payload after header")
	}

	decrypted := itlDecrypt(hdr, payload)
	decompressed, _, err := itlInflate(decrypted)
	if err != nil {
		return fmt.Errorf("decompressing ITL payload: %w", err)
	}

	if len(decompressed) < 4 {
		return fmt.Errorf("decrypted ITL payload too short")
	}

	tag := readTag(decompressed, 0)
	validTags := map[string]bool{"hdsm": true, "msdh": true, "htim": true, "hohm": true}
	if !validTags[tag] {
		return fmt.Errorf("invalid first chunk tag after decryption: %q", tag)
	}

	return nil
}
