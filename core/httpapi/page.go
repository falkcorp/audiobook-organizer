// file: core/httpapi/page.go
// version: 1.0.0
// guid: 4246d293-1342-4d74-be74-ffb598673ff0
// last-edited: 2026-11-07

package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"iter"
	"net/http"
)

// Page is the DEFAULT list response (05-http-api §4.4): HEAD's ListResponse
// shape (internal/httputil/types.go:23-29), so the UI keeps page counts. Total
// is always present (SIMP-8).
type Page[T any] struct {
	Items  []T   `json:"items"`
	Count  int   `json:"count"`
	Limit  int   `json:"limit"`
	Offset int   `json:"offset"`
	Total  int64 `json:"total"`
}

// PageParams is embedded in list request types. Binding normalises it as
// HEAD's ParsePaginationParams does: a limit below 1 becomes 50, above 1000
// becomes 1000, a negative offset becomes 0.
type PageParams struct {
	Limit  int `query:"limit"`  // default 50, max 1000 (internal/httputil/parse.go:74-90)
	Offset int `query:"offset"` // default 0
}

func (p *PageParams) normalize() {
	p.Limit = clampLimit(p.Limit)
	if p.Offset < 0 {
		p.Offset = 0
	}
}

// CursorPage is the OPT-IN list response for append-only feeds (audit,
// journal).
type CursorPage[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

// CursorParams is embedded in cursor-paged request types; its limit is
// normalised like PageParams'.
type CursorParams struct {
	Limit  int    `query:"limit"`
	Cursor string `query:"cursor"` // opaque
}

func (p *CursorParams) normalize() { p.Limit = clampLimit(p.Limit) }

func clampLimit(n int) int {
	switch {
	case n < 1:
		return defaultPageLimit
	case n > maxPageLimit:
		return maxPageLimit
	default:
		return n
	}
}

// EncodeCursor returns base64url(pos || filterHash). No HMAC: a forged
// position only starts a scan elsewhere, and every row is permission-checked
// as it is read (SIMP-8).
func EncodeCursor(pos []byte, filterHash uint64) string {
	b := make([]byte, 0, len(pos)+8)
	b = append(b, pos...)
	b = binary.BigEndian.AppendUint64(b, filterHash)
	return base64.RawURLEncoding.EncodeToString(b)
}

// DecodeCursor returns the position of a cursor EncodeCursor made with the
// same filterHash. A malformed cursor, or one presented with a different
// filter, is a 400 invalid_cursor *Error.
func DecodeCursor(s string, filterHash uint64) (pos []byte, err error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(b) < 8 {
		return nil, invalidCursor("malformed cursor")
	}
	if binary.BigEndian.Uint64(b[len(b)-8:]) != filterHash {
		return nil, invalidCursor("cursor was issued for different filters")
	}
	return bytes.Clone(b[:len(b)-8]), nil
}

func invalidCursor(detail string) *Error {
	return &Error{Status: http.StatusBadRequest, Code: "invalid_cursor", Detail: detail,
		Fields: []FieldError{Field("/query/cursor", "invalid_cursor", detail)}}
}

// Positioned is one item of a cursor-paged feed with its storage or sort key.
type Positioned[T any] struct {
	Pos  []byte // storage key or sort key
	Item T
}

// PaginateCursor drives seq into a CursorPage, stopping at limit+1. It checks
// p.Cursor against filterHash first (invalid_cursor on a mismatch); the
// caller has already started seq at the cursor's position (DecodeCursor).
// NextCursor carries the position of the first item not returned, so the next
// page starts there inclusively; it is empty on the last page.
func PaginateCursor[T any](ctx context.Context, p CursorParams, seq iter.Seq2[Positioned[T], error], filterHash uint64) (CursorPage[T], error) {
	if p.Cursor != "" {
		if _, err := DecodeCursor(p.Cursor, filterHash); err != nil {
			return CursorPage[T]{}, err
		}
	}
	limit := clampLimit(p.Limit)
	page := CursorPage[T]{Items: make([]T, 0, min(limit, defaultPageLimit))}
	for it, err := range seq {
		if err != nil {
			return CursorPage[T]{}, err
		}
		if err := ctx.Err(); err != nil {
			return CursorPage[T]{}, err
		}
		if len(page.Items) == limit {
			page.NextCursor = EncodeCursor(it.Pos, filterHash)
			break
		}
		page.Items = append(page.Items, it.Item)
	}
	return page, nil
}
