// file: internal/itunes/itl.go
// version: 1.9.0
// guid: 7f2a8b3c-4d5e-6f01-a2b3-c4d5e6f7a8b9
// last-edited: 2026-07-17

package itunes

import (
	"bytes"
	"compress/zlib"
	"crypto/aes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"sort"
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

// ITLWriteBackResult contains results of updating an ITL file.
type ITLWriteBackResult struct {
	UpdatedCount int
	BackupPath   string
	OutputPath   string
	// UpdatedPersistentIDs lists the (lowercased hex) persistent IDs whose
	// location blocks were actually rewritten. Populated by
	// UpdateITLLocations only; callers use it to mark per-row bookkeeping
	// (e.g. deferred-update rows) applied ONLY for rows really written
	// (DL-5) — a requested PID absent from the ITL never appears here.
	UpdatedPersistentIDs []string
}

// ITLNewTrack describes a track to insert into an ITL file.
type ITLNewTrack struct {
	Location    string
	Name        string
	Album       string
	Artist      string
	Genre       string
	Kind        string // e.g. "MPEG audio file", "AAC audio file"
	Size        int
	TotalTime   int // milliseconds
	TrackNumber int
	DiscNumber  int
	Year        int
	BitRate     int
	SampleRate  int
}

// ITLNewPlaylist describes a playlist to insert into an ITL file.
type ITLNewPlaylist struct {
	Title    string
	TrackIDs []int // Song IDs to include
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

func writeUint32LE(buf []byte, offset int, val uint32) {
	buf[offset] = byte(val)
	buf[offset+1] = byte(val >> 8)
	buf[offset+2] = byte(val >> 16)
	buf[offset+3] = byte(val >> 24)
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

// pidToHexLE converts a PID stored in little-endian byte order (v10+ ITL)
// to the same hex string format used in the XML (big-endian / MSB first).
func pidToHexLE(pid [8]byte) string {
	reversed := [8]byte{pid[7], pid[6], pid[5], pid[4], pid[3], pid[2], pid[1], pid[0]}
	return hex.EncodeToString(reversed[:])
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

func itlEncrypt(hdr *hdfmHeader, data []byte) []byte {
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
		block.Encrypt(out[i:i+bs], data[i:i+bs])
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

func itlDeflate(data []byte) []byte {
	var buf bytes.Buffer
	// Use BestSpeed (level 1) to match iTunes' compression more closely.
	// iTunes appears to use a low compression level. Using Go's default (level 6)
	// produces smaller but different output that iTunes rejects.
	w, err := zlib.NewWriterLevel(&buf, zlib.BestSpeed)
	if err != nil {
		// Fallback to default if level fails
		w = zlib.NewWriter(&buf)
	}
	_, _ = w.Write(data)
	_ = w.Close()
	return buf.Bytes()
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

// encodeHohmString encodes a string for writing into a hohm record.
// Returns (encoded bytes, encoding flag).
// If all runes <= 0xFF, uses Windows-1252 (flag 3). Otherwise UTF-16BE (flag 1).
func encodeHohmString(s string) ([]byte, byte) {
	allLatin := true
	for _, r := range s {
		if r > 0xFF {
			allLatin = false
			break
		}
	}

	if allLatin {
		enc := charmap.Windows1252.NewEncoder()
		out, err := enc.Bytes([]byte(s))
		if err != nil {
			// Fallback to UTF-16BE
			return encodeUTF16BE(s), 1
		}
		return out, 3
	}

	return encodeUTF16BE(s), 1
}

func encodeUTF16BE(s string) []byte {
	runes := []rune(s)
	buf := make([]byte, len(runes)*2)
	for i, r := range runes {
		binary.BigEndian.PutUint16(buf[i*2:i*2+2], uint16(r))
	}
	return buf
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

// rewriteChunksLE dispatches to the LE implementation in itl_le.go.
func rewriteChunksLE(data []byte, updateMap map[string]string) ([]byte, int) {
	return rewriteChunksLEMatched(data, updateMap, nil)
}

// rewriteChunksLEMatched is rewriteChunksLE plus per-PID accounting: when
// matched is non-nil, every persistent ID (lowercased hex) whose location
// block was actually rewritten is recorded into it. Lets callers
// distinguish "requested" from "actually written" PIDs (DL-5).
func rewriteChunksLEMatched(data []byte, updateMap map[string]string, matched map[string]bool) ([]byte, int) {
	return rewriteChunksLEImpl(data, updateMap, matched)
}

// ---------------------------------------------------------------------------
// Chunk builders for write path
// ---------------------------------------------------------------------------

// buildHohmChunk builds a hohm chunk for a given type and string value.
func buildHohmChunk(hohmType uint32, value string) []byte {
	encodedStr, encFlag := encodeHohmString(value)
	chunkLen := 40 + len(encodedStr)
	buf := make([]byte, chunkLen)
	copy(buf[0:4], "hohm")
	writeUint32BE(buf, 4, uint32(chunkLen))
	writeUint32BE(buf, 8, uint32(chunkLen))
	writeUint32BE(buf, 12, hohmType)
	buf[16+11] = encFlag
	writeUint32BE(buf, 28, uint32(len(encodedStr)))
	// bytes 32-39 are zero (already)
	copy(buf[40:], encodedStr)
	return buf
}

// buildHtimChunk builds a 156-byte htim chunk for a new track.
func buildHtimChunk(trackID int, track ITLNewTrack) []byte {
	htimLen := 156
	buf := make([]byte, htimLen)
	copy(buf[0:4], "htim")
	writeUint32BE(buf, 4, uint32(htimLen))
	writeUint32BE(buf, 8, uint32(htimLen)) // recordLength
	writeUint32BE(buf, 16, uint32(trackID))
	writeUint32BE(buf, 36, uint32(track.Size))
	writeUint32BE(buf, 40, uint32(track.TotalTime))
	writeUint32BE(buf, 44, uint32(track.TrackNumber))
	if track.Year > 0 {
		binary.BigEndian.PutUint16(buf[54:56], uint16(track.Year))
	}
	if track.BitRate > 0 {
		binary.BigEndian.PutUint16(buf[58:60], uint16(track.BitRate))
	}
	if track.SampleRate > 0 {
		binary.BigEndian.PutUint16(buf[60:62], uint16(track.SampleRate))
	}
	buf[104] = byte(track.DiscNumber)
	// Random persistent ID
	var pid [8]byte
	_, _ = rand.Read(pid[:])
	copy(buf[128:136], pid[:])
	return buf
}

// buildHpimChunk builds an hpim chunk for a new playlist.
func buildHpimChunk(itemCount int) []byte {
	// Minimum hpim: 20 bytes header + 428 bytes remaining (for persistent ID at [420:428])
	hpimLen := 20 + 428
	buf := make([]byte, hpimLen)
	copy(buf[0:4], "hpim")
	writeUint32BE(buf, 4, uint32(hpimLen))
	writeUint32BE(buf, 8, uint32(hpimLen)) // recordLength
	writeUint32BE(buf, 16, uint32(itemCount))
	// Random persistent ID at remaining[420:428] = offset 20+420 = 440
	var pid [8]byte
	_, _ = rand.Read(pid[:])
	copy(buf[440:448], pid[:])
	return buf
}

// buildHptmChunk builds an hptm chunk referencing a track ID.
func buildHptmChunk(trackID int) []byte {
	hptmLen := 28
	buf := make([]byte, hptmLen)
	copy(buf[0:4], "hptm")
	writeUint32BE(buf, 4, uint32(hptmLen))
	// 16 unknown bytes at [8:24] (zero)
	writeUint32BE(buf, 24, uint32(trackID))
	return buf
}

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

// ---------------------------------------------------------------------------
// UpdateITLLocations — the write path (ProcessLibrary port)
// ---------------------------------------------------------------------------

// UpdateITLLocations reads an ITL file, updates file locations for the specified
// persistent IDs, and writes the result to outputPath.
func UpdateITLLocations(inputPath, outputPath string, updates []ITLLocationUpdate) (*ITLWriteBackResult, error) {
	if len(updates) == 0 {
		return &ITLWriteBackResult{OutputPath: outputPath}, nil
	}

	// Build lookup map
	updateMap := make(map[string]string, len(updates))
	for _, u := range updates {
		updateMap[strings.ToLower(u.PersistentID)] = u.NewLocation
	}

	// Mutation: rewrite location hohms in the decompressed LE payload. Header
	// regeneration (CRIT-3) + the full ITLSafetyContract + BE refusal all happen
	// inside the SafeWriteITL chokepoint (TASK-004) — no direct writeITLFile here.
	updatedCount := 0
	matched := make(map[string]bool, len(updates))
	mutate := func(decompressed []byte) ([]byte, error) {
		var newData []byte
		newData, updatedCount = rewriteChunksLEMatched(decompressed, updateMap, matched)
		return newData, nil
	}
	result, err := safeWriteOrEncodeToFile(inputPath, outputPath, mutate, &updatedCount)
	if err != nil {
		return nil, err
	}
	// Surface which requested PIDs were actually rewritten (DL-5): callers
	// must not mark bookkeeping applied for PIDs that never matched a
	// location block in this ITL.
	result.UpdatedPersistentIDs = make([]string, 0, len(matched))
	for pid := range matched {
		result.UpdatedPersistentIDs = append(result.UpdatedPersistentIDs, pid)
	}
	sort.Strings(result.UpdatedPersistentIDs)
	return result, nil
}

// safeWriteOrEncodeToFile routes an itl.go writeback entry point through the
// SafeWriteITL atomic protocol when writing in place (inputPath == outputPath),
// or through the safe in-memory encode (header regen + full contract) written to
// outputPath when the caller wants a distinct output file (e.g. a managed .tmp).
// `updated`, when non-nil, is surfaced as ITLWriteBackResult.UpdatedCount —
// callers set it from inside their mutate closure (the closure runs before this
// function returns, so the deref below sees the final value).
func safeWriteOrEncodeToFile(inputPath, outputPath string, mutate func([]byte) ([]byte, error), updated *int) (*ITLWriteBackResult, error) {
	if inputPath == outputPath {
		if _, err := SafeWriteITL(inputPath, mutate); err != nil {
			return nil, err
		}
		return &ITLWriteBackResult{UpdatedCount: derefCount(updated), OutputPath: outputPath}, nil
	}
	raw, err := os.ReadFile(inputPath)
	if err != nil {
		return nil, fmt.Errorf("reading ITL: %w", err)
	}
	outBytes, err := safeEncodeITL(raw, mutate, DefaultContractConfig())
	if err != nil {
		return nil, err
	}
	if err := writeFileSync(outputPath, outBytes); err != nil {
		return nil, fmt.Errorf("writing ITL: %w", err)
	}
	fixITLPermissions(outputPath)
	return &ITLWriteBackResult{UpdatedCount: derefCount(updated), OutputPath: outputPath}, nil
}

func derefCount(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// ---------------------------------------------------------------------------
// InsertITLTracks — insert new tracks into an ITL file
// ---------------------------------------------------------------------------

// InsertITLTracks reads an ITL file, appends new tracks after existing ones,
// and writes the result to outputPath.
func InsertITLTracks(inputPath, outputPath string, tracks []ITLNewTrack) (*ITLWriteBackResult, error) {
	if len(tracks) == 0 {
		return &ITLWriteBackResult{OutputPath: outputPath}, nil
	}

	// Mutation: append new track chunks at the track-insert offset. Header
	// regeneration (CRIT-3) + the full ITLSafetyContract + BE refusal happen in
	// the SafeWriteITL chokepoint (TASK-004).
	mutate := func(decompressed []byte) ([]byte, error) {
		// Find max track ID and the insertion point (after last track hohm,
		// before first hpim).
		maxID := findMaxTrackID(decompressed)
		insertOffset := findTrackInsertOffset(decompressed)

		var newChunks bytes.Buffer
		for i, tr := range tracks {
			trackID := maxID + 1 + i
			newChunks.Write(buildHtimChunk(trackID, tr))
			// Order matters: location first, then metadata (iTunes convention).
			if tr.Location != "" {
				newChunks.Write(buildHohmChunk(0x0D, tr.Location))
			}
			if tr.Name != "" {
				newChunks.Write(buildHohmChunk(0x02, tr.Name))
			}
			if tr.Album != "" {
				newChunks.Write(buildHohmChunk(0x03, tr.Album))
			}
			if tr.Artist != "" {
				newChunks.Write(buildHohmChunk(0x04, tr.Artist))
			}
			if tr.Genre != "" {
				newChunks.Write(buildHohmChunk(0x05, tr.Genre))
			}
			if tr.Kind != "" {
				newChunks.Write(buildHohmChunk(0x06, tr.Kind))
			}
		}

		var newData bytes.Buffer
		newData.Write(decompressed[:insertOffset])
		newData.Write(newChunks.Bytes())
		newData.Write(decompressed[insertOffset:])
		return newData.Bytes(), nil
	}
	count := len(tracks)
	return safeWriteOrEncodeToFile(inputPath, outputPath, mutate, &count)
}

// findMaxTrackID walks chunks to find the highest track ID.
func findMaxTrackID(data []byte) int {
	maxID := 0
	offset := 0
	for offset+8 <= len(data) {
		tag := readTag(data, offset)
		if tag == "" {
			break
		}
		length := int(readUint32BE(data, offset+4))
		if length < 8 || offset+length > len(data) {
			break
		}
		if tag == "htim" && offset+20 <= len(data) {
			id := int(readUint32BE(data, offset+16))
			if id > maxID {
				maxID = id
			}
		}
		offset += length
	}
	return maxID
}

// findTrackInsertOffset finds the byte offset where new tracks should be inserted.
// This is after the last track-related chunk (htim or hohm following htim) and
// before any playlist chunk (hpim).
func findTrackInsertOffset(data []byte) int {
	offset := 0
	lastTrackEnd := 0
	inTrackSection := false
	for offset+8 <= len(data) {
		tag := readTag(data, offset)
		if tag == "" {
			break
		}
		length := int(readUint32BE(data, offset+4))
		if length < 8 || offset+length > len(data) {
			break
		}
		switch tag {
		case "htim":
			inTrackSection = true
			lastTrackEnd = offset + length
		case "hohm":
			if inTrackSection {
				lastTrackEnd = offset + length
			}
		case "hpim":
			// Playlist section starts here; insert before it
			if lastTrackEnd > 0 {
				return lastTrackEnd
			}
			return offset
		}
		offset += length
	}
	if lastTrackEnd > 0 {
		return lastTrackEnd
	}
	return len(data)
}

// ---------------------------------------------------------------------------
// RewriteITLExtensions — rewrite file extensions in all location hohms
// ---------------------------------------------------------------------------

// RewriteITLExtensions reads an ITL file and replaces file extensions in all
// location strings (hohm 0x0D and 0x0B).
func RewriteITLExtensions(inputPath, outputPath string, oldExt, newExt string) (*ITLWriteBackResult, error) {
	// Normalize extensions to include dot
	if !strings.HasPrefix(oldExt, ".") {
		oldExt = "." + oldExt
	}
	if !strings.HasPrefix(newExt, ".") {
		newExt = "." + newExt
	}

	// Mutation: rewrite file extensions in location hohms. Header regeneration
	// (CRIT-3) + the full ITLSafetyContract + BE refusal happen in the
	// SafeWriteITL chokepoint (TASK-004). (BE was already refused here pre-T004;
	// the chokepoint enforces it for every entry point uniformly.)
	count := 0
	mutate := func(decompressed []byte) ([]byte, error) {
		var newData []byte
		newData, count = rewriteExtensionsInChunks(decompressed, oldExt, newExt)
		return newData, nil
	}
	return safeWriteOrEncodeToFile(inputPath, outputPath, mutate, &count)
}

// rewriteExtensionsInChunks walks chunks and rewrites extensions in location hohms.
func rewriteExtensionsInChunks(data []byte, oldExt, newExt string) ([]byte, int) {
	var out bytes.Buffer
	offset := 0
	count := 0

	for offset+8 <= len(data) {
		tag := readTag(data, offset)
		if tag == "" {
			out.Write(data[offset:])
			break
		}
		length := int(readUint32BE(data, offset+4))
		if length < 8 || offset+length > len(data) {
			out.Write(data[offset:])
			break
		}

		if tag == "hohm" && length >= 40 {
			hohmType := int(readUint32BE(data, offset+12))
			if hohmType == 0x0D || hohmType == 0x0B {
				// Read current string
				encodingFlag := data[offset+16+11]
				strDataLen := int(readUint32BE(data, offset+28))
				strStart := offset + 40
				if strStart+strDataLen <= offset+length && strStart+strDataLen <= len(data) {
					s, err := decodeHohmString(data[strStart:strStart+strDataLen], encodingFlag)
					if err == nil && strings.HasSuffix(strings.ToLower(s), strings.ToLower(oldExt)) {
						newLoc := s[:len(s)-len(oldExt)] + newExt
						rewritten := rewriteHohmLocationBE(data, offset, length, newLoc)
						out.Write(rewritten)
						count++
						offset += length
						continue
					}
				}
			}
		}

		out.Write(data[offset : offset+length])
		offset += length
	}

	return out.Bytes(), count
}

// ---------------------------------------------------------------------------
// InsertITLPlaylist — insert a new playlist into an ITL file
// ---------------------------------------------------------------------------

// InsertITLPlaylist reads an ITL file, appends a new playlist, and writes
// the result to outputPath.
func InsertITLPlaylist(inputPath, outputPath string, playlist ITLNewPlaylist) (*ITLWriteBackResult, error) {
	// Mutation: append the new playlist chunks. Header regeneration (CRIT-3) +
	// the full ITLSafetyContract + BE refusal happen in the SafeWriteITL
	// chokepoint (TASK-004).
	mutate := func(decompressed []byte) ([]byte, error) {
		var plChunks bytes.Buffer
		plChunks.Write(buildHpimChunk(len(playlist.TrackIDs)))
		plChunks.Write(buildHohmChunk(0x64, playlist.Title))
		for _, tid := range playlist.TrackIDs {
			plChunks.Write(buildHptmChunk(tid))
		}
		var newData bytes.Buffer
		newData.Write(decompressed)
		newData.Write(plChunks.Bytes())
		return newData.Bytes(), nil
	}
	count := 1
	return safeWriteOrEncodeToFile(inputPath, outputPath, mutate, &count)
}

// writeITLFile handles compression, encryption, and writing of an ITL file.
// Uses 0664 permissions and runs setfacl to restore a permissive ACL mask
// so the file remains accessible via SMB to iTunes.
// encodeITLPayload compresses, encrypts, and wraps payload in a new hdfm header,
// returning the final ITL file bytes without writing to disk.
// Used by the in-memory export path (BuildExportITL / Task 033).
func encodeITLPayload(hdr *hdfmHeader, payload []byte, compress bool) ([]byte, error) {
	var finalPayload []byte
	if compress {
		finalPayload = itlDeflate(payload)
	} else {
		finalPayload = payload
	}
	encrypted := itlEncrypt(hdr, finalPayload)
	newFileLen := uint32(len(encrypted)) + hdr.headerLen
	newHeader := buildHdfmHeader(hdr.version, hdr.headerRemainder, newFileLen, hdr.unknown)
	out := make([]byte, 0, len(newHeader)+len(encrypted))
	out = append(out, newHeader...)
	out = append(out, encrypted...)
	return out, nil
}

func writeITLFile(outputPath string, hdr *hdfmHeader, payload []byte, compress bool, count int) (*ITLWriteBackResult, error) {
	var finalPayload []byte
	if compress {
		finalPayload = itlDeflate(payload)
	} else {
		finalPayload = payload
	}

	encrypted := itlEncrypt(hdr, finalPayload)

	newFileLen := uint32(len(encrypted)) + hdr.headerLen
	newHeader := buildHdfmHeader(hdr.version, hdr.headerRemainder, newFileLen, hdr.unknown)

	outData := make([]byte, 0, len(newHeader)+len(encrypted))
	outData = append(outData, newHeader...)
	outData = append(outData, encrypted...)

	if err := os.WriteFile(outputPath, outData, 0664); err != nil {
		return nil, fmt.Errorf("writing ITL: %w", err)
	}

	// Fix ACL mask after write so the file is accessible via SMB to iTunes
	fixITLPermissions(outputPath)

	return &ITLWriteBackResult{
		UpdatedCount: count,
		OutputPath:   outputPath,
	}, nil
}

// fixITLPermissions sets permissive ACL mask on an ITL file so it's accessible
// via SMB shares. Silently ignores errors (setfacl might not be available,
// or the user might not have permission to change ACLs).
func fixITLPermissions(path string) {
	// setfacl sets the ACL mask so effective permissions include rw for all ACL entries
	if cmd := osexec.Command("setfacl", "-m", "mask::rw", path); cmd != nil {
		_ = cmd.Run() // best-effort — may fail if setfacl not installed or no permission
	}
	_ = os.Chmod(path, 0664) // best-effort — may fail if not owner
}

// RenameITLFile renames an ITL file and fixes permissions/ACLs afterward.
// Use this instead of os.Rename for ITL files to preserve SMB accessibility.
// If the rename fails (e.g., target is locked by iTunes), returns the error
// without cleaning up — the .tmp file remains for manual recovery or retry.
func RenameITLFile(oldPath, newPath string) error {
	if err := os.Rename(oldPath, newPath); err != nil {
		return fmt.Errorf("ITL rename failed (target may be locked by iTunes): %w", err)
	}
	fixITLPermissions(newPath)
	return nil
}
