// file: core/internal/httpmw/requestid.go
// version: 1.0.0
// guid: 35d59a5d-d0ce-4ad4-83ef-dc0cf6d156e1
// last-edited: 2026-11-08

package httpmw

import (
	"encoding/binary"
	"net/http"
	"strconv"
	"time"
)

// requestIDHeader is stage 1's request and response header.
const requestIDHeader = "X-Request-ID"

// requestID is stage 1 (05-http-api §4.2, §4.7): a fresh ULID for every
// request, echoed in X-Request-ID; a well-formed inbound X-Request-ID
// (^[A-Za-z0-9._-]{1,64}$) is kept as client_request_id, never as the ID.
func (c *Chain) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := c.newRequestID()
		if st := From(r.Context()); st != nil {
			st.requestID = id
			if in := r.Header.Get(requestIDHeader); wellFormedRequestID(in) {
				st.clientRequestID = in
			}
		}
		w.Header().Set(requestIDHeader, id)
		next.ServeHTTP(w, r)
	})
}

// wellFormedRequestID reports ^[A-Za-z0-9._-]{1,64}$.
func wellFormedRequestID(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		b := s[i]
		switch {
		case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9', b == '.', b == '_', b == '-':
		default:
			return false
		}
	}
	return true
}

// crockford is the ULID alphabet (Crockford's base32).
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// newRequestID returns a ULID: 48 bits of Unix milliseconds, 80 random
// bits, 26 characters of Crockford base32. When the random source fails it
// falls back to the monotonic local-<n> (§6: correlation only, not
// security).
func (c *Chain) newRequestID() string {
	var b [16]byte
	if _, err := c.rand(b[6:]); err != nil {
		return "local-" + strconv.FormatUint(c.fallbackSeq.Add(1), 10)
	}
	ms := uint64(time.Now().UnixMilli())
	var ts [8]byte
	binary.BigEndian.PutUint64(ts[:], ms)
	copy(b[:6], ts[2:])
	return encodeULID(b)
}

// encodeULID writes the 128 bits of b as 26 base32 characters, the first
// carrying the top 3 bits.
func encodeULID(b [16]byte) string {
	hi := binary.BigEndian.Uint64(b[:8])
	lo := binary.BigEndian.Uint64(b[8:])
	var out [26]byte
	for i := 25; i >= 0; i-- {
		out[i] = crockford[lo&31]
		lo = lo>>5 | hi<<59
		hi >>= 5
	}
	return string(out[:])
}
