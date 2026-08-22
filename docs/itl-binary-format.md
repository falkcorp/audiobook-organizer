<!-- file: docs/itl-binary-format.md -->
<!-- version: 1.0.0 -->
<!-- guid: d297cd25-b25e-4134-a7ee-a5d50dd11846 -->
<!-- last-edited: 2026-08-22 -->

# iTunes Library (ITL) Binary Format Reference

Version: 1.0.0  
Last verified against: iTunes 12.13.10.3 (Windows)  
Sources: titl (Java/josephw), libitlp (C/jeanthom), itunes-rs (Rust/MattRyder), mrexodia blog posts, and direct testing

## Overview

The `.itl` file has three layers:

1. **hdfm header** — unencrypted, always big-endian
2. **AES-128-ECB encrypted payload** (partial for v10+)
3. **zlib-compressed payload** (BestSpeed level required for v12+)

Processing: read hdfm → decrypt → decompress → parse msdh sequence.

## hdfm Header (always big-endian)

| Offset | Size | Field |
|--------|------|-------|
| 0x00 | 4 | `"hdfm"` signature |
| 0x04 | 4 | Header length (typically 96 or 144 bytes) |
| 0x08 | 4 | File length (validated against disk) |
| 0x0C | 4 | Unknown flags |
| 0x10 | 1 | Version string length (N) |
| 0x11 | N | Version string (e.g. `"12.13.10.3"`) |
| 0x30 | 4 | Number of msdh blocks (within remainder) |
| 0x5C | 4 | maxCryptSize (0 = encrypt all) |

**Header remainder** (bytes after version string) contains:
- Library persistent ID (8 bytes, after leading zeros)
- Track count at remainder offset 41 (BE uint32)
- Playlist count at remainder offset 45 (BE uint32)
- maxCryptSize at absolute offset 0x5C

**Important:** Modifying the header remainder track/playlist counts causes iTunes to reject the file as damaged. The counts must match the actual content OR be left unchanged (iTunes tolerates minor mismatches in some cases but not zero-vs-nonzero).

## Encryption

- **Algorithm:** AES-128-ECB, no padding
- **Key:** `BHUILuilfghuila3` (16 bytes, hardcoded)
- **iTunes < 10:** Entire payload encrypted
- **iTunes >= 10:** Only first `min(payloadSize, maxCryptSize)` bytes encrypted; typically 102,400 bytes (100 KB). Remainder stored in plaintext.
- Encrypted length rounded down to 16-byte boundary

## Compression

- **Algorithm:** zlib deflate
- **Level:** Must use **BestSpeed (level 1)** for iTunes 12+. DefaultCompression produces zlib flag byte 0x9C which iTunes rejects. BestSpeed produces flag byte 0x01.
- **Detection:** zlib magic byte 0x78 at start of decrypted data
- Applied before encryption (compress then encrypt)

## Endianness (v10+ Breaking Change)

- **iTunes < 10:** Payload uses big-endian. Tags: `hdsm`, `htim`, `hohm`, `hpim`, `hptm`
- **iTunes >= 10:** Payload uses little-endian. Tags appear reversed: `msdh`, `mith`, `mhoh`, `miph`, `mtph`
- **hdfm header is ALWAYS big-endian** regardless of version
- Detection: first 4 bytes of decompressed data = `"msdh"` → LE format

## msdh Container Blocks

After decompression, the payload is a flat sequence of msdh blocks.

### msdh Header (LE format)

| Offset | Size | Field |
|--------|------|-------|
| 0 | 4 | `"msdh"` |
| 4 | 4 | Header length (typically 96) |
| 8 | 4 | Total length (header + content) |
| 12 | 4 | Block type |
| 16+ | padding to header length (zeros) |

### Block Types

| Type | Constant | Content | Description |
|------|----------|---------|-------------|
| 1 | BLOCK_MLTH | mlth → mith* → mhoh* | **Track list** |
| 2 | BLOCK_MLPH | mlph → miph* → mhoh* + mtph* | **Playlist list** |
| 4 | BLOCK_FILE | file path string | Library path; signals EOF |
| 9 | BLOCK_MLAH | mlah → miah* → mhoh* | Artists/albums |
| 11 | BLOCK_MLIH | mlih → miih* → mhoh* | Item info |
| 12 | BLOCK_MHGH | mhgh → mhoh* | Global settings |
| 13 | BLOCK_MLTH_ALT | mlth (alternate) | Alternate track list |
| 14 | BLOCK_MLPH_ALT | mlph (alternate) | Alternate playlist list |
| 15 | BLOCK_MLRH | mlrh → mprh* | Unknown |
| 16 | BLOCK_MFDH | mfdh | App version string |
| 19 | | | Unknown (seen in v12.13) |
| 20 | BLOCK_MLQH | mlqh → mhoh* + miqh* | Unknown |
| 21 | BLOCK_MLSH | mlsh → msph* → mhoh* | Unknown |
| 22 | | | Unknown (seen in v12.13) |
| 23 | BLOCK_STSH | stsh | **Possible signature/checksum** |

### List Headers (mlth, mlph, etc.)

Each list block has a record count at offset +8 (LE uint32).

## Track Block (mith)

Standard header: 156 bytes.

| Offset | Size | Field |
|--------|------|-------|
| 0 | 4 | `"mith"` |
| 4 | 4 | Header length (156) |
| 8 | 4 | **Total length (includes all mhoh sub-blocks)** |
| 12 | 4 | Number of mhoh sub-blocks |
| 16 | 4 | Track ID |
| 20 | 4 | Block type (usually 1) |
| 28 | 4 | Mac OS file type |
| 32 | 4 | Modification date (Mac epoch) |
| 36 | 4 | File size (bytes) |
| 40 | 4 | Playtime (ms) |
| 44 | 2 | Track number |
| 48 | 2 | Total tracks |
| 54 | 2 | Year |
| 58 | 2 | Bit rate (kbps) |
| 60 | 2 | Sample rate (Hz) |
| 64 | 4 | Volume adjustment (signed) |
| 68 | 4 | Start time (ms) |
| 72 | 4 | End time (ms) |
| 76 | 4 | Play count |
| 82 | 2 | Compilation flag |
| 100 | 4 | Last play date (Mac epoch) |
| 104 | 2 | Disc number |
| 106 | 2 | Total discs |
| 108 | 1 | Rating (0-100) |
| 110 | 1 | Unchecked flag |
| 120 | 4 | Date added (Mac epoch) |
| 128 | 8 | **Persistent ID** (LE: reversed byte order vs XML hex) |
| 300 | 8 | Album Persistent ID (if header > 308) |

**Critical:** `totalLen` (offset 8) MUST include all following mhoh sub-blocks. Setting it to just the header length (156) causes iTunes to reject the file.

## mhoh Metadata Block

| Offset | Size | Field |
|--------|------|-------|
| 0 | 4 | `"mhoh"` |
| 4 | 4 | Header/total length |
| 8 | 4 | Total length |
| 12 | 4 | hohm type |
| 27 | 1 | Encoding flag (0=ASCII, 1=UTF-16BE, 2=UTF-8) |
| 28 | 4 | String data length |
| 32 | 8 | Zero padding |
| 40 | N | String data |

### hohm Type Catalog (Track)

| Type | Content |
|------|---------|
| 0x02 | Track title |
| 0x03 | Album title |
| 0x04 | Artist |
| 0x05 | Genre |
| 0x06 | Kind/file type |
| 0x08 | Comments |
| 0x0B | Local path as URL |
| 0x0C | Composer |
| 0x0D | **File location** (must be FIRST after mith) |
| 0x12 | Subtitle |
| 0x1A | Studio/Producer |
| 0x2B | ISRC |
| 0x2E | Copyright |

### hohm Type Catalog (Playlist)

| Type | Content |
|------|---------|
| 0x64 | Playlist title |
| 0x65 | Smart criteria (binary) |
| 0x66 | Smart info (binary) |

**Order matters:** Location (0x0D) must come first after the mith header, before other metadata. iTunes rejects files where Name (0x02) appears before Location.

## Validated Operations (tested against iTunes 12.13.10.3)

| Operation | Status | Notes |
|-----------|--------|-------|
| Round-trip (decrypt→recompress→encrypt) | ✅ Works | No content changes |
| Location update (rewrite mhoh 0x0D) | ✅ Works | Production write-back path |
| Add tracks (LE mith+mhoh insertion) | ✅ Works | Must set mith totalLen correctly |
| Remove tracks (strip last N mith) | ✅ Works | Update mlth count + msdh totalLen |
| Add then remove | ✅ Works | Combined operations |
| Strip all tracks (blank library) | ❌ Fails | Internal consistency checks (stsh checksum?) |
| Strip tracks + playlists | ❌ Fails | Same issue |
| Strip all msdh content | ❌ Fails | Same issue |
| Zero header remainder counts | ❌ Fails | iTunes validates counts vs content |

## Date Format

Mac epoch: seconds since January 1, 1904 00:00:00 UTC.

## Key Implementation Files

- `internal/itunes/itl.go` — hdfm parsing, encryption, compression, BE chunk walking
- `internal/itunes/itl_be.go` — Big-endian chunk parser/rewriter
- `internal/itunes/itl_le.go` — Little-endian chunk parser
- `internal/itunes/itl_le_mutate.go` — LE track insertion/removal
- `internal/itunes/generate_test_itls.go` — Test ITL generation from production template
