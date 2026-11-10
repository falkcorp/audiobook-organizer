#!/usr/bin/env python3
# file: scripts/r2corpus/pebble_bytes.py
# version: 1.1.0
# guid: 6b0f3c57-2a7e-4d8e-9f41-c3a5e0d7b912
# last-edited: 2026-11-10
"""The plaintext bytes of the corpus' store files, for the leak scan (F2-12).

A raw byte scan of the store directory misses what the stores compress or split:

- SST (``*.sst``) and blob (``*.blob``) files hold their data in blocks that
  are usually Snappy-compressed, so a secret written into a value is not in the
  file's raw bytes.
- The WAL (``*.log``) cuts every batch into chunks of at most one 32 KiB log
  block, each behind a chunk header, so a value that straddles a block boundary
  is split by 7, 11 or 19 header bytes.
- A bleve zap segment (``*.zap``) Snappy-compresses each document's stored
  fields.

``plaintext_views(path)`` returns the byte strings to scan for one file: always
the raw bytes, plus every decompressed block of an SST/blob file, the
reassembled chunk payloads of a WAL, and every document's stored fields of a zap
segment. It **fails closed**: a block it cannot walk or decompress raises
``StoreDecodeError`` instead of being skipped, so a store the scanner cannot
read is never declared clean.

``latest_record(store_dir, prefix)`` finds the newest stored JSON value that
starts with ``prefix`` (the writer's post-stop check of its run rows).

Stdlib only (CI's Python has no snappy module): the Snappy decoder and the
block walker are written out here.
"""

from __future__ import annotations

import json
import pathlib
import struct

# Pebble's table magic (sstable/format.go): the last 8 bytes of every SST.
PEBBLE_MAGIC = bytes.fromhex("f09faab3f09faab3")
# Footer lengths, magic included, of the table formats that end in PEBBLE_MAGIC:
# the 53-byte RocksDB-style footer and the 61-byte footer of the newer formats
# (R0's tables have the 61-byte one). What follows the last block must be
# exactly one of these.
FOOTER_LENGTHS = frozenset({53, 61})
MAX_FOOTER = max(FOOTER_LENGTHS)
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
    be a footer of a known length ending in Pebble's magic. Anything else
    raises: a checksummed block of a compression type this decoder does not
    read, or a tail that is not a footer.

    The checksum is tried for every type byte, not only the two this decoder
    reads, so a final block of another compression type is found and refused
    instead of being mistaken for the footer.
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
            want = int.from_bytes(data[e + 1 : e + TRAILER], "little")
            if want == mask_crc(nxt ^ 0xFFFFFFFF):
                end = e
                break
            c = nxt
        if end < 0:
            # No further block: what is left must be the footer. Walking on to
            # here (rather than stopping MAX_FOOTER short of the end) is what
            # reaches the small index/metaindex blocks just before the footer.
            if len(data) - start not in FOOTER_LENGTHS:
                raise StoreDecodeError(
                    f"no block trailer after offset {start}, and the "
                    f"{len(data) - start}-byte tail is not a known footer"
                )
            break
        if data[end] not in (COMPRESSION_NONE, COMPRESSION_SNAPPY):
            raise StoreDecodeError(f"block at {start} has compression type {data[end]}")
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


# --------------------------------------------------------------- zap segments

# The zap version R0's bleve writes (blevesearch/zapx v17). Its footer, from
# the end: crc u32, version u32, chunk mode u32, sections index offset u64,
# stored index offset u64, doc count u64, writer-id length u32, writer id.
ZAP_VERSION = 17
_ZAP_FIXED_FOOTER = 4 + 4 + 4 + 8 + 8 + 8 + 4


def _uvarint_at(buf: bytes, pos: int) -> tuple[int, int]:
    value = shift = 0
    while True:
        if pos >= len(buf) or shift > 63:
            raise StoreDecodeError("zap: truncated varint")
        b = buf[pos]
        pos += 1
        value |= (b & 0x7F) << shift
        if b < 0x80:
            return value, pos
        shift += 7


def zap_stored_fields(data: bytes) -> list[bytes]:
    """Every document's stored fields of a zap v17 segment, decompressed.

    Per document (zapx new.go): uvarint meta length, uvarint data length, the
    meta (which starts with the _id value's length), then the data: the _id
    value as is, followed by the Snappy-compressed other stored fields. The
    document offsets are u64s at the stored index offset.
    """
    if len(data) < _ZAP_FIXED_FOOTER:
        raise StoreDecodeError("zap: shorter than its footer")
    version = struct.unpack(">I", data[-8:-4])[0]
    if version != ZAP_VERSION:
        raise StoreDecodeError(f"zap: version {version}, this decoder reads {ZAP_VERSION}")
    stored_index = struct.unpack(">Q", data[-28:-20])[0]
    num_docs = struct.unpack(">Q", data[-36:-28])[0]
    if stored_index + 8 * num_docs > len(data):
        raise StoreDecodeError("zap: the stored index runs past the file")
    out = []
    for i in range(num_docs):
        off = struct.unpack(">Q", data[stored_index + 8 * i : stored_index + 8 * i + 8])[0]
        if off >= stored_index:
            raise StoreDecodeError(f"zap: document {i} starts past the stored section")
        meta_len, pos = _uvarint_at(data, off)
        data_len, pos = _uvarint_at(data, pos)
        meta = data[pos : pos + meta_len]
        id_len, _ = _uvarint_at(meta, 0)
        body = data[pos + meta_len : pos + meta_len + data_len]
        if len(body) != data_len or id_len > data_len:
            raise StoreDecodeError(f"zap: document {i} runs past the stored section")
        out.append(body[:id_len] + b"\n" + snappy_decompress(body[id_len:]))
    return out


# --------------------------------------------------------------- stored records


def latest_record(store: pathlib.Path, prefix: bytes) -> dict | None:
    """The newest JSON object in a Pebble store whose bytes start with ``prefix``.

    The WAL holds the writes after the last flush, in order, so its last match
    is the newest; without one, the value comes from the tables, and a match in
    more than one table is ambiguous (the store's level order is not read here)
    and raises.
    """
    dec = json.JSONDecoder()

    def scan(buf: bytes) -> list[dict]:
        found, pos = [], buf.find(prefix)
        while pos >= 0:
            try:
                obj, _ = dec.raw_decode(buf[pos:].decode("utf-8", "replace"))
            except ValueError as e:
                raise StoreDecodeError(f"a record at {pos} is not JSON: {e}") from e
            found.append(obj)
            pos = buf.find(prefix, pos + 1)
        return found

    wal_hits: list[dict] = []
    for log in sorted(store.glob("*.log")):
        wal_hits += scan(wal_payload(log.read_bytes()))
    if wal_hits:
        return wal_hits[-1]
    table_hits = {}
    for sst in sorted(store.glob("*.sst")):
        hits = [h for _, _, plain in table_blocks(sst.read_bytes()) for h in scan(plain)]
        if hits:
            table_hits[sst.name] = hits[-1]
    if len(table_hits) > 1:
        raise StoreDecodeError(f"{prefix!r} is in several tables: {sorted(table_hits)}")
    return next(iter(table_hits.values()), None)


# --------------------------------------------------------------- per file


def plaintext_views(path: pathlib.Path) -> list[bytes]:
    """The byte strings a leak scan must search for one store file."""
    data = path.read_bytes()
    views = [data]
    if path.suffix in (".sst", ".blob"):
        views += [plain for _, _, plain in table_blocks(data)]
    elif path.suffix == ".log" and path.parent.name.endswith(".pebble"):
        views.append(wal_payload(data))
    elif path.suffix == ".zap":
        views += zap_stored_fields(data)
    return views
