// file: internal/itunes/mhoh_encoding_audit.go
// version: 1.0.0
// guid: 2c46832f-3418-4be7-8e80-2184a8ec9d63

// AuditMhohEncoding walks every mhoh string block in an iTunes .itl payload
// and collects per-hohmType histograms of the byte fields that govern string
// encoding. The resulting MhohAuditReport is the empirical ground truth from
// which ITunesMhohEncoding (mhoh_encoding_table.go) is derived.
//
// Fields examined per mhoh block (offsets relative to block start):
//
//	+4  u32 LE  headerLen     — iTunes-authored: always 24
//	+8  u32 LE  totalLen      — must equal 40 + strDataLen per SPEC 2
//	+12 u32 LE  hohmType      — the field semantic (0x02=Name, 0x0B=LocalURL, etc.)
//	+24 u32 LE  at24          — iTunes' actual encoding indicator (values 1/2/3 observed)
//	+27 byte    at27          — our old "encoding flag"; iTunes writes 0x00 always
//	+28 u32 LE  strDataLen    — byte length of the string payload starting at +40
//	+32..+39    8 bytes       — iTunes writes these as 0x00 always
//
// Container types walked: 1 (tracks), 2 (playlists), 9 (albums), 11 (artists).
package itunes

import (
	"fmt"
	"sort"
	"strconv"
	"time"
)

// msdhContainerTypes is the list of msdh blockType values that contain mhoh
// string metadata in an iTunes-authored library. Types 1 and 2 (tracks and
// playlists) are well-known; 9 (albums / miah) and 11 (artists / miih) are
// included per TASK-002 scope.
var msdhContainerTypes = []int{1, 2, 9, 11}

// MhohTypeAudit holds the per-hohmType histogram data collected by one run of
// AuditMhohEncoding. All map keys are the string representation of the numeric
// value for JSON-friendliness (e.g., "24", "0").
type MhohTypeAudit struct {
	// HohmTypeHex is the hohmType rendered as "0x02" etc. for readability.
	HohmTypeHex string `json:"hohm_type_hex"`
	// Count is the total number of mhoh blocks of this type seen.
	Count int `json:"count"`
	// HeaderLenValues is a histogram of the +4 headerLen field values.
	// iTunes-authored libraries show exclusively "24" here.
	HeaderLenValues map[string]int `json:"header_len_values"`
	// At24Values is a histogram of the u32 at byte +24 — iTunes' encoding
	// indicator. Values 1 (Windows-1252 / ASCII-compatible), 2 (UTF-8 or
	// pure-ASCII), and 3 (UTF-16LE) have been observed in golden corpus data.
	At24Values map[string]int `json:"at24_values"`
	// At27Values is a histogram of the byte at +27. iTunes writes 0x00
	// exclusively; our old encodeHohmString wrote 1 or 3 (CRIT-1).
	At27Values map[string]int `json:"at27_values"`
	// TailZero histograms whether bytes +32..+39 are all 0x00 ("true"/"false").
	TailZero map[string]int `json:"tail_zero"`
	// LenArithmeticOK histograms whether totalLen == 40 + strDataLen.
	LenArithmeticOK map[string]int `json:"len_arithmetic_ok"`
}

// MhohAuditReport is the top-level result of AuditMhohEncoding.
type MhohAuditReport struct {
	// LibraryPath is the path supplied by the caller.
	LibraryPath string `json:"library_path"`
	// LibraryVersion is the version string from the hdfm header.
	LibraryVersion string `json:"library_version"`
	// AuditDate is the ISO-8601 date the audit was run.
	AuditDate string `json:"audit_date"`
	// TotalMhohBlocks is the total number of mhoh blocks visited across all
	// container types.
	TotalMhohBlocks int `json:"total_mhoh_blocks"`
	// TotalContainersWalked counts msdh containers of the target types found.
	TotalContainersWalked int `json:"total_containers_walked"`
	// PerType maps uint32 hohmType (as decimal string) to its histogram.
	PerType map[string]*MhohTypeAudit `json:"per_type"`
}

// AuditMhohEncoding decrypts, inflates, and walks the payload of an iTunes
// .itl file, collecting per-hohmType encoding field histograms. The libPath
// string is embedded in the report metadata verbatim.
//
// Returns an error only for fatal decryption/inflation failures; partial data
// (e.g., truncated containers) is tolerated and counted.
func AuditMhohEncoding(fileData []byte, libPath string) (*MhohAuditReport, error) {
	// Decrypt and inflate the payload using the same pipeline as
	// DecryptAndInflateITL — reuse the exported helper.
	payload, err := DecryptAndInflateITL(fileData)
	if err != nil {
		return nil, fmt.Errorf("decrypt/inflate: %w", err)
	}

	// Extract the library version from the hdfm header for the provenance
	// comment in the constants file.
	hdr, hdrErr := parseHdfmHeader(fileData)
	libVersion := "(unknown)"
	if hdrErr == nil {
		libVersion = hdr.version
	}

	report := &MhohAuditReport{
		LibraryPath:    libPath,
		LibraryVersion: libVersion,
		AuditDate:      time.Now().UTC().Format("2006-01-02"),
		PerType:        make(map[string]*MhohTypeAudit),
	}

	// Walk each target msdh container type.
	for _, ct := range msdhContainerTypes {
		off, hdrLen, totalLen := findMsdhByType(payload, ct)
		if off < 0 {
			// Container type absent in this library — not an error.
			continue
		}
		report.TotalContainersWalked++
		contentStart := off + hdrLen
		contentEnd := min(off+totalLen, len(payload))
		walkContainerForMhoh(payload, contentStart, contentEnd, report)
	}

	return report, nil
}

// walkContainerForMhoh recursively descends into the chunk tree rooted at
// [start, end) and records every mhoh block it encounters.
func walkContainerForMhoh(data []byte, start, end int, report *MhohAuditReport) {
	offset := start
	for offset+12 <= end {
		tag := readTag(data, offset)
		if tag == "" {
			break
		}
		if offset+8 > len(data) {
			break
		}
		headerLen := int(readUint32LE(data, offset+4))
		totalLen := int(readUint32LE(data, offset+8))

		// Determine effective chunk size to advance the walker.
		//
		// Only chunks that carry sub-content (mhoh, mith, miah, miph, miih) use
		// totalLen for advancement. Header-only chunks (mlth and similar) store a
		// SEMANTIC count at +8, not a byte length, so they MUST advance by
		// headerLen only — using totalLen for them would skip hundreds of MB of
		// track data.
		//
		// For mhoh: totalLen = 40 + strDataLen. We MUST use totalLen to advance
		// past the string payload; using headerLen (24) would land us inside the
		// string bytes and corrupt every subsequent tag read.
		chunkSize := headerLen
		isDataChunk := (tag == "mhoh" || tag == "mith" || tag == "miah" || tag == "miph" || tag == "miih") &&
			totalLen >= headerLen && offset+totalLen <= end
		if isDataChunk {
			chunkSize = totalLen
		}
		if chunkSize < 8 || offset+chunkSize > end {
			break
		}

		switch tag {
		case "mhoh":
			// Record the encoding fields of this string block.
			recordMhohBlock(data, offset, totalLen, report)

		case "mith", "miah", "miph", "miih":
			// Descend into child blocks at [offset+headerLen, offset+chunkSize).
			// When totalLen == headerLen (mith with no sub-mhoh children), the
			// range is empty and we recurse vacuously.
			childStart := offset + headerLen
			childEnd := min(offset+chunkSize, end)
			if childStart < childEnd {
				walkContainerForMhoh(data, childStart, childEnd, report)
			}
		}

		offset += chunkSize
	}
}

// recordMhohBlock extracts the encoding-relevant fields from one mhoh block
// and updates the per-type histograms in the report.
func recordMhohBlock(data []byte, offset, totalLen int, report *MhohAuditReport) {
	// A valid mhoh must be at least 40 bytes so we can safely read all fields.
	if offset+40 > len(data) {
		return
	}

	headerLen := int(readUint32LE(data, offset+4))
	hohmType := readUint32LE(data, offset+12)
	at24 := readUint32LE(data, offset+24)
	at27 := data[offset+27]
	strDataLen := int(readUint32LE(data, offset+28))

	// Check whether the 8 bytes at +32..+39 are all zero.
	tailZero := true
	for i := 32; i < 40; i++ {
		if offset+i >= len(data) {
			tailZero = false
			break
		}
		if data[offset+i] != 0 {
			tailZero = false
			break
		}
	}

	// Length arithmetic: totalLen should equal 40 + strDataLen.
	lenArithOK := totalLen == 40+strDataLen

	key := strconv.FormatUint(uint64(hohmType), 10)
	e, ok := report.PerType[key]
	if !ok {
		e = &MhohTypeAudit{
			HohmTypeHex:     fmt.Sprintf("0x%02X", hohmType),
			HeaderLenValues: make(map[string]int),
			At24Values:      make(map[string]int),
			At27Values:      make(map[string]int),
			TailZero:        make(map[string]int),
			LenArithmeticOK: make(map[string]int),
		}
		report.PerType[key] = e
	}

	e.Count++
	report.TotalMhohBlocks++
	e.HeaderLenValues[strconv.Itoa(headerLen)]++
	e.At24Values[strconv.FormatUint(uint64(at24), 10)]++
	e.At27Values[strconv.Itoa(int(at27))]++
	if tailZero {
		e.TailZero["true"]++
	} else {
		e.TailZero["false"]++
	}
	if lenArithOK {
		e.LenArithmeticOK["true"]++
	} else {
		e.LenArithmeticOK["false"]++
	}
}

// DeriveEncodingTable converts an MhohAuditReport into the canonical
// ITunesMhohEncoding map consumed by the mhoh-format guard (T003). Only types
// where every observed headerLen is 24 are included; types with non-uniform
// headerLen values are excluded with a warning.
//
// The AllowedAt24 set is populated from the observed +24 values in the corpus;
// At27Uniform is true when every +24 is 0 (invariant in all iTunes libraries).
func DeriveEncodingTable(report *MhohAuditReport) map[uint32]MhohEncodingEntry {
	table := make(map[uint32]MhohEncodingEntry)

	// Sort keys for deterministic output.
	typeKeys := make([]string, 0, len(report.PerType))
	for k := range report.PerType {
		typeKeys = append(typeKeys, k)
	}
	sort.Strings(typeKeys)

	for _, k := range typeKeys {
		audit := report.PerType[k]
		typeNum, err := strconv.ParseUint(k, 10, 32)
		if err != nil {
			continue
		}
		hohmType := uint32(typeNum)

		// Only include types where headerLen is uniformly 24.
		if len(audit.HeaderLenValues) != 1 {
			continue
		}
		if _, ok := audit.HeaderLenValues["24"]; !ok {
			continue
		}

		// Collect all observed +24 values.
		var allowed []uint32
		at24Keys := make([]string, 0, len(audit.At24Values))
		for k2 := range audit.At24Values {
			at24Keys = append(at24Keys, k2)
		}
		sort.Strings(at24Keys)
		for _, k2 := range at24Keys {
			v, err2 := strconv.ParseUint(k2, 10, 32)
			if err2 == nil {
				allowed = append(allowed, uint32(v))
			}
		}

		// at27Uniform: true when all observed +27 values are 0.
		at27Uniform := len(audit.At27Values) == 1
		if _, ok := audit.At27Values["0"]; !ok {
			at27Uniform = false
		}

		table[hohmType] = MhohEncodingEntry{
			HeaderLen:   24,
			AllowedAt24: allowed,
			At27Uniform: at27Uniform,
			Count:       audit.Count,
		}
	}
	return table
}
