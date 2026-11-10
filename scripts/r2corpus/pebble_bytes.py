#!/usr/bin/env python3
# file: scripts/r2corpus/pebble_bytes.py
# version: 1.0.0
# guid: 6b0f3c57-2a7e-4d8e-9f41-c3a5e0d7b912
# last-edited: 2026-11-10
"""The plaintext bytes of a Pebble store's files, for the corpus leak scan (F2-12).

A raw byte scan of a Pebble directory misses what Pebble compresses or splits:

- SST (``*.sst``) and blob (``*.blob``) files hold their data in blocks that
  are usually Snappy-compressed, so a secret written into a value is not in the
  file's raw bytes.
- The WAL (``*.log``) cuts every batch into chunks of at most one 32 KiB log
  block, each behind a chunk header, so a value that straddles a block boundary
  is split by 7 or 11 header bytes.

``plaintext_views(path)`` returns the byte strings to scan for one file: always
the raw bytes, plus every decompressed block of an SST/blob file, plus the
reassembled chunk payloads of a WAL. It **fails closed**: a block it cannot walk
or decompress raises ``StoreDecodeError`` instead of being skipped, so a store
the scanner cannot read is never declared clean.

Stdlib only (CI's Python has no snappy module): the Snappy decoder and the
block walker are written out here.
"""

from __future__ import annotations

import pathlib

# Pebble's table magic (sstable/format.go): the last 8 bytes of every SST.
PEBBLE_MAGIC = bytes.fromhex("f09faab3f09faab3")
# The longest footer any table format writes (v6 adds attributes to the 53-byte
# RocksDB-style footer); what follows the last block must fit in this.
MAX_FOOTER = 128
# Block trailer: one compression-type byte, then a 4-byte checksum.
TRAILER = 5
COMPRESSION_NONE = 0
COMPRESSION_SNAPPY = 1
WAL_BLOCK = 32 * 1024


class StoreDecodeError(RuntimeError):
    """A store file the scanner cannot walk: the scan must not call it clean."""


# --------------------------------------------------------------- CRC32C


def _crc32c_table() -> list[int]:
    table = []
    for i in range(256):
        c = i
        for _ in range(8):
            c = (c >> 1) ^ 0x82F63B78 if c & 1 else c >> 1
        table.append(c)
    return table


_CRC_TABLE = _crc32c_table()


def crc32c(data: bytes, crc: int = 0) -> int:
    c = crc ^ 0xFFFFFFFF
    for b in data:
        c = _CRC_TABLE[(c ^ b) & 0xFF] ^ (c >> 8)
    return c ^ 0xFFFFFFFF


def mask_crc(c: int) -> int:
    """LevelDB/Pebble's masked CRC (a CRC of data that holds CRCs)."""
    return ((((c >> 15) | (c << 17)) & 0xFFFFFFFF) + 0xA282EAD8) & 0xFFFFFFFF


# --------------------------------------------------------------- Snappy


def _uvarint(buf: bytes, pos: int) -> tuple[int, int]:
    value = shift = 0
    while True:
        if pos >= len(buf) or shift > 63:
            raise StoreDecodeError("snappy: truncated length varint")
        b = buf[pos]
        pos += 1
        value |= (b & 0x7F) << shift
        if b < 0x80:
            return value, pos
        shift += 7


def snappy_decompress(buf: bytes) -> bytes:
    """Decode one raw Snappy block (the format Pebble's block compressor writes)."""
    want, pos = _uvarint(buf, 0)
    out = bytearray()
    n = len(buf)
    while pos < n:
        tag = buf[pos]
        pos += 1
        kind = tag & 3
        if kind == 0:  # literal
            length = tag >> 2
            if length >= 60:
                extra = length - 59
                if pos + extra > n:
                    raise StoreDecodeError("snappy: truncated literal length")
                length = int.from_bytes(buf[pos : pos + extra], "little")
                pos += extra
            length += 1
            if pos + length > n:
                raise StoreDecodeError("snappy: literal runs past the block")
            out += buf[pos : pos + length]
            pos += length
            continue
        if kind == 1:
            if pos + 1 > n:
                raise StoreDecodeError("snappy: truncated copy-1")
            length = 4 + ((tag >> 2) & 7)
            offset = ((tag >> 5) << 8) | buf[pos]
            pos += 1
        elif kind == 2:
            if pos + 2 > n:
                raise StoreDecodeError("snappy: truncated copy-2")
            length = 1 + (tag >> 2)
            offset = int.from_bytes(buf[pos : pos + 2], "little")
            pos += 2
        else:
            if pos + 4 > n:
                raise StoreDecodeError("snappy: truncated copy-4")
            length = 1 + (tag >> 2)
            offset = int.from_bytes(buf[pos : pos + 4], "little")
            pos += 4
        if offset == 0 or offset > len(out):
            raise StoreDecodeError("snappy: copy offset outside the output")
        start = len(out) - offset
        for i in range(length):  # byte by byte: a copy may overlap its own output
            out.append(out[start + i])
    if len(out) != want:
        raise StoreDecodeError(f"snappy: decoded {len(out)} bytes, header says {want}")
    return bytes(out)


# --------------------------------------------------------------- SST blocks


def table_blocks(data: bytes) -> list[tuple[int, int, bytes]]:
    """Every block of an SST/blob file as (offset, compression type, plaintext).

    Pebble writes blocks back to back from offset 0, each followed by a 5-byte
    trailer whose checksum (masked CRC32C) covers the block and its type byte.
    The walk finds each block's end by that checksum, so it needs neither the
    index nor the table format's version; whatever follows the last block must
    be a footer ending in Pebble's magic. Anything else raises.
    """
    if len(data) < len(PEBBLE_MAGIC) or data[-len(PEBBLE_MAGIC) :] != PEBBLE_MAGIC:
        raise StoreDecodeError("no Pebble table magic at the end of the file")
    blocks: list[tuple[int, int, bytes]] = []
    start = 0
    limit = len(data) - len(PEBBLE_MAGIC)
    while start < limit:
        c = 0xFFFFFFFF
        end = -1
        for e in range(start, limit - TRAILER + 1):
            typ = data[e]
            nxt = _CRC_TABLE[(c ^ typ) & 0xFF] ^ (c >> 8)
            if typ in (COMPRESSION_NONE, COMPRESSION_SNAPPY):
                want = int.from_bytes(data[e + 1 : e + TRAILER], "little")
                if want == mask_crc(nxt ^ 0xFFFFFFFF):
                    end = e
                    break
            c = nxt
        if end < 0:
            # No further block: what is left must be the footer. Walking on to
            # here (rather than stopping MAX_FOOTER short of the end) is what
            # reaches the small index/metaindex blocks just before the footer.
            if limit - start > MAX_FOOTER:
                raise StoreDecodeError(f"no block trailer after offset {start}")
            break
        raw = data[start:end]
        plain = snappy_decompress(raw) if data[end] == COMPRESSION_SNAPPY else raw
        blocks.append((start, data[end], plain))
        start = end + TRAILER
    if not blocks:
        raise StoreDecodeError("a table with no blocks")
    return blocks


# --------------------------------------------------------------- WAL chunks

# Chunk types (record/record.go): full/first/middle/last, then the recyclable
# variants with a 4-byte log number, then the WAL-sync variants with an 8-byte
# offset as well.
_LEGACY = {1, 2, 3, 4}
_RECYCLABLE = {5, 6, 7, 8}
_SYNC = {9, 10, 11, 12}


def wal_payload(data: bytes) -> bytes:
    """The WAL's chunk payloads concatenated, headers removed.

    A zero-filled tail (preallocated space, the end of a block too short for a
    header) ends a block. An unknown chunk type raises.
    """
    out = bytearray()
    for block_start in range(0, len(data), WAL_BLOCK):
        block = data[block_start : block_start + WAL_BLOCK]
        pos = 0
        while pos + 7 <= len(block):
            typ = block[pos + 6]
            length = int.from_bytes(block[pos + 4 : pos + 6], "little")
            if typ == 0 and length == 0:
                break  # zero padding: the rest of this block is empty
            if typ in _LEGACY:
                hdr = 7
            elif typ in _RECYCLABLE:
                hdr = 11
            elif typ in _SYNC:
                hdr = 19
            else:
                raise StoreDecodeError(
                    f"WAL chunk type {typ} at offset {block_start + pos} is unknown"
                )
            if pos + hdr + length > len(block):
                raise StoreDecodeError(
                    f"WAL chunk at offset {block_start + pos} runs past its block"
                )
            out += block[pos + hdr : pos + hdr + length]
            pos += hdr + length
    return bytes(out)


# --------------------------------------------------------------- per file


def plaintext_views(path: pathlib.Path) -> list[bytes]:
    """The byte strings a leak scan must search for one store file."""
    data = path.read_bytes()
    views = [data]
    if path.suffix in (".sst", ".blob"):
        views += [plain for _, _, plain in table_blocks(data)]
    elif path.suffix == ".log" and path.parent.name.endswith(".pebble"):
        views.append(wal_payload(data))
    return views
