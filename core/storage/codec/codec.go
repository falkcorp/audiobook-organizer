// file: core/storage/codec/codec.go
// version: 1.0.1
// guid: 2acc8d4a-3d33-42b8-ac68-1e9cd60ed5c0
// last-edited: 2026-11-07

// Package codec holds the generic value codecs of core storage (05-storage
// §5S.3.3): an L0 leaf that imports nothing from core, so storage.Typed binds
// a codec to a keyspace without codec importing storage (MOD3-9).
//
// Native codecs write a one-byte format tag first, so a value can change its
// encoding without a key-format change: 'j' for JSON, 'r' for raw bytes, 'z'
// for a zstd-compressed inner encoding. LegacyJSON is the one codec without a
// tag: it is byte-compatible with the rows an app wrote before core existed.
package codec

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/klauspost/compress/zstd"
)

// Codec[T] encodes one value type. Native codecs write a 1-byte format tag
// first so a value can change encoding without a key-format change.
type Codec[T any] interface {
	Encode(v T) ([]byte, error)
	Decode(b []byte) (T, error)
}

// ErrUnknownVersion: a JSON value carries a version its codec was not built
// for (or none at all).
var ErrUnknownVersion = errors.New("codec: unknown value version")

// The native format tags.
const (
	tagJSON = 'j'
	tagRaw  = 'r'
	tagZstd = 'z'
)

// maxDecoded bounds what one zstd value may decompress to: a value is split
// above 4 MiB (05-storage §5S.3.3), so anything near this is corrupt input,
// not data, and must not cost the process its memory.
const maxDecoded = 256 << 20

// JSON is the native JSON codec: the tag 'j', then the value's JSON object
// with the version in its "v" member, {"v":version,...}. Decode rejects a
// value of any other version, or of none, with ErrUnknownVersion.
//
// T must encode to a JSON object (a struct or a map), and that object must
// not have a member "v" of its own, which the envelope owns: Encode refuses
// either with an error rather than writing a value Decode could misread.
func JSON[T any](version uint8) Codec[T] { return jsonCodec[T]{version: version} }

type jsonCodec[T any] struct{ version uint8 }

func (c jsonCodec[T]) Encode(v T) ([]byte, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("codec: JSON encode: %w", err)
	}
	body = bytes.TrimSpace(body)
	if len(body) < 2 || body[0] != '{' {
		return nil, fmt.Errorf("codec: JSON v%d: %T does not encode to a JSON object", c.version, v)
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(body, &members); err != nil {
		return nil, fmt.Errorf("codec: JSON encode: %w", err)
	}
	if _, clash := members["v"]; clash {
		return nil, fmt.Errorf("codec: JSON v%d: %T has a member \"v\", which the version envelope owns", c.version, v)
	}
	// The envelope header is at most 12 bytes; append grows out for the body.
	// No len(body)+n capacity: that sum is an allocation-size overflow shape.
	out := make([]byte, 0, 12)
	out = append(out, tagJSON)
	out = fmt.Appendf(out, `{"v":%d`, c.version)
	if len(members) > 0 {
		out = append(out, ',')
	}
	out = append(out, body[1:]...)
	return out, nil
}

func (c jsonCodec[T]) Decode(b []byte) (T, error) {
	var zero T
	if len(b) == 0 || b[0] != tagJSON {
		return zero, fmt.Errorf("codec: JSON v%d: %s", c.version, describeTag(b))
	}
	var env struct {
		V *uint8 `json:"v"`
	}
	if err := json.Unmarshal(b[1:], &env); err != nil {
		return zero, fmt.Errorf("codec: JSON v%d decode: %w", c.version, err)
	}
	if env.V == nil || *env.V != c.version {
		return zero, fmt.Errorf("codec: JSON value version %s, want %d: %w", versionString(env.V), c.version, ErrUnknownVersion)
	}
	var v T
	if err := json.Unmarshal(b[1:], &v); err != nil {
		return zero, fmt.Errorf("codec: JSON v%d decode: %w", c.version, err)
	}
	return v, nil
}

// Raw is the native codec for bytes as they are: the tag 'r', then the bytes.
func Raw() Codec[[]byte] { return rawCodec{} }

type rawCodec struct{}

func (rawCodec) Encode(v []byte) ([]byte, error) {
	out := make([]byte, 0, len(v)+1)
	out = append(out, tagRaw)
	return append(out, v...), nil
}

func (rawCodec) Decode(b []byte) ([]byte, error) {
	if len(b) == 0 || b[0] != tagRaw {
		return nil, fmt.Errorf("codec: raw: %s", describeTag(b))
	}
	return bytes.Clone(b[1:]), nil
}

// Zstd compresses inner's encoding with zstd (klauspost/compress) when it is
// at least minSize bytes, behind the tag 'z'; a shorter encoding is stored
// as inner wrote it, with inner's own tag. Decode reads either.
//
// The codec builds its zstd encoder and decoder on first use and keeps them
// (both are safe for concurrent EncodeAll/DecodeAll), so build one codec per
// value type and reuse it rather than one per value.
func Zstd[T any](inner Codec[T], minSize int) Codec[T] {
	return &zstdCodec[T]{inner: inner, minSize: minSize}
}

type zstdCodec[T any] struct {
	inner   Codec[T]
	minSize int

	once sync.Once
	enc  *zstd.Encoder
	dec  *zstd.Decoder
	err  error
}

func (c *zstdCodec[T]) Encode(v T) ([]byte, error) {
	b, err := c.inner.Encode(v)
	if err != nil {
		return nil, err
	}
	if len(b) < c.minSize {
		return b, nil
	}
	if err := c.coders(); err != nil {
		return nil, err
	}
	out := make([]byte, 1, len(b)/2+1)
	out[0] = tagZstd
	return c.enc.EncodeAll(b, out), nil
}

func (c *zstdCodec[T]) Decode(b []byte) (T, error) {
	if len(b) == 0 || b[0] != tagZstd {
		return c.inner.Decode(b)
	}
	var zero T
	if err := c.coders(); err != nil {
		return zero, err
	}
	plain, err := c.dec.DecodeAll(b[1:], nil)
	if err != nil {
		return zero, fmt.Errorf("codec: zstd decode: %w", err)
	}
	return c.inner.Decode(plain)
}

// coders builds the codec's encoder and decoder once.
func (c *zstdCodec[T]) coders() error {
	c.once.Do(func() {
		if c.enc, c.err = zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1)); c.err != nil {
			c.err = fmt.Errorf("codec: zstd encoder: %w", c.err)
			return
		}
		if c.dec, c.err = zstd.NewReader(nil, zstd.WithDecoderConcurrency(0), zstd.WithDecoderMaxMemory(maxDecoded)); c.err != nil {
			c.err = fmt.Errorf("codec: zstd decoder: %w", c.err)
		}
	})
	return c.err
}

// LegacyJSON is the codec for rows of an app's pre-core families: books'
// domain families, which stay in place, and the HEAD rows a cutover step
// decodes (§5S.3.13). It is encoding/json (v1) with no tag byte and no
// envelope, byte-compatible with what HEAD wrote (DATA-5). encoding/json/v2
// defaults (case-sensitive names, omitzero, nil-slice encoding) would
// silently change how those rows decode.
func LegacyJSON[T any]() Codec[T] { return legacyJSONCodec[T]{} }

type legacyJSONCodec[T any] struct{}

func (legacyJSONCodec[T]) Encode(v T) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("codec: legacy JSON encode: %w", err)
	}
	return b, nil
}

func (legacyJSONCodec[T]) Decode(b []byte) (T, error) {
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		var zero T
		return zero, fmt.Errorf("codec: legacy JSON decode: %w", err)
	}
	return v, nil
}

// describeTag names what a value that lacks the wanted tag starts with.
func describeTag(b []byte) string {
	if len(b) == 0 {
		return "empty value"
	}
	return fmt.Sprintf("value has format tag %q", b[0])
}

func versionString(v *uint8) string {
	if v == nil {
		return "absent"
	}
	return fmt.Sprintf("%d", *v)
}
