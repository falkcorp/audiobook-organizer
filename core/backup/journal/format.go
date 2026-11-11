// file: core/backup/journal/format.go
// version: 1.0.0
// guid: 3327485a-f95b-499f-bda1-dca7958d7feb
// last-edited: 2026-11-11

package journal

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/falkcorp/mediacore/entity"
)

// This file is the authenticated line format of the revocation journal
// (05-backup §5B.3.6 row "Format and integrity", SEC2-9). The backup
// directory is untrusted storage, so every line carries its own proof:
//
//	line 0   {"seq":0,"install_id":..,"app":..,"started_at":..,"kid":..,"base_seq":..,"base_mac":..,"mac":..}
//	line n   {"seq":n,...a journal.Revocation...,"kid":..,"mac":..}
//
// with mac = HMAC-SHA256(DeriveKeyFor(kid, macInfo), prev_mac ‖ canonical),
// where prev_mac is the previous line's mac (the header's base_mac, empty for
// a journal that never dropped a line, before the header's own line) and
// canonical is the line's stored bytes with the trailing mac member cut off.
//
// The MAC is taken over the exact bytes that are stored, never over a
// re-encoding of the decoded fields: mac is always the last member, the
// canonical bytes are the line up to it with the closing brace put back, and
// verification splits the stored line the same way. No encoder detail (time
// formatting, a nil slice against an empty one, field order) can therefore
// make a verified line differ from the line that was signed, and a byte
// edited anywhere in a line, a member added or the whitespace changed,
// breaks it.

// macInfo is the HKDF info of the journal's MAC key (05-backup §5B.3.6).
const macInfo = "core.backup/revocations/v1"

// macHexLen is the length of a mac in hex: HMAC-SHA256.
const macHexLen = 2 * sha256.Size

// macMember is what precedes the mac's hex in a stored line.
const macMember = `,"mac":"`

// Keys is how the journal reaches the derive key without importing secrets
// (05-backup §5B.3.2's import list; the kernel adapts *secrets.Store to it).
// It is the part of 05-config §2.3's Store the journal needs.
type Keys interface {
	// ActiveDeriveID is the ID of the derive key new lines are written under
	// ("d1"); a rotation changes it mid-journal without breaking the chain.
	ActiveDeriveID() string
	// DeriveKeyFor derives info's key from the derive key kid. It fails (the
	// secrets store's ErrKeyUnavailable) for a retired or unknown kid; the
	// journal then reports the line that names it, never panics.
	DeriveKeyFor(kid, info string) ([]byte, error)
}

// Header is the journal's line 0 (§5B.3.6). BaseSeq and BaseMAC are the last
// line retention dropped (0 and "" for a journal that dropped none), so the
// chain continues after a prune: the first line after the header has seq
// BaseSeq+1 and chains from the header's own mac.
type Header struct {
	InstallID string
	App       string
	StartedAt time.Time
	KID       string
	BaseSeq   uint64
	BaseMAC   string
	MAC       string // verified; not set by the writer's input
}

// wireHeader is the header's JSON form without its mac.
type wireHeader struct {
	Seq       uint64    `json:"seq"`
	InstallID string    `json:"install_id"`
	App       string    `json:"app"`
	StartedAt time.Time `json:"started_at"`
	KID       string    `json:"kid"`
	BaseSeq   uint64    `json:"base_seq"`
	BaseMAC   string    `json:"base_mac"`
}

// wireLine is a revocation line's JSON form without its mac.
type wireLine struct {
	Seq        uint64     `json:"seq"`
	At         time.Time  `json:"at"`
	Kind       Kind       `json:"kind"`
	Subject    entityJSON `json:"subject"`
	Object     entityJSON `json:"object"`
	Detail     []string   `json:"detail,omitempty"`
	Epoch      int64      `json:"epoch,omitempty"`
	RestoreSeq uint64     `json:"restore_seq,omitempty"`
	Source     string     `json:"source"`
	KID        string     `json:"kid"`
}

// entityJSON is entity.Ref's form inside a line.
type entityJSON struct {
	Module string `json:"module,omitempty"`
	Kind   string `json:"kind,omitempty"`
	ID     string `json:"id,omitempty"`
}

func (e entityJSON) ref() entity.Ref { return entity.Ref(e) }

// Line is one verified line of the chain: the revocation, the derive key ID it
// was signed under and its mac.
type Line struct {
	Revocation
	KID string
	MAC string
}

// sign is the mac of body chained after prevMAC under key.
func sign(key []byte, prevMAC string, body []byte) string {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(prevMAC))
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// macEqual compares two macs in constant time.
func macEqual(a, b string) bool { return hmac.Equal([]byte(a), []byte(b)) }

// seal turns canonical (a JSON object) and its mac into the stored line,
// newline-terminated: the object with the mac as its last member.
func seal(canonical []byte, mac string) []byte {
	out := make([]byte, 0, len(canonical)+len(macMember)+macHexLen+3)
	out = append(out, canonical[:len(canonical)-1]...)
	out = append(out, macMember...)
	out = append(out, mac...)
	return append(out, '"', '}', '\n')
}

// errNoMAC is a complete line whose tail is not a well-formed mac member.
var errNoMAC = errors.New("the line does not end in a mac member")

// unseal splits a stored line (without its newline) into its canonical bytes
// and its mac: the exact inverse of seal.
func unseal(line []byte) (canonical []byte, mac string, err error) {
	tail := len(macMember) + macHexLen + 2
	if len(line) < tail+1 || line[len(line)-2] != '"' || line[len(line)-1] != '}' {
		return nil, "", errNoMAC
	}
	hexStart := len(line) - 2 - macHexLen
	if string(line[hexStart-len(macMember):hexStart]) != macMember {
		return nil, "", errNoMAC
	}
	mac = string(line[hexStart : hexStart+macHexLen])
	for i := 0; i < len(mac); i++ {
		if c := mac[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return nil, "", errNoMAC
		}
	}
	canonical = make([]byte, 0, hexStart-len(macMember)+1)
	canonical = append(canonical, line[:hexStart-len(macMember)]...)
	return append(canonical, '}'), mac, nil
}

// canonicalLine is the one encoding of a revocation line before its mac:
// r.Seq and kid are written as given.
func canonicalLine(r Revocation, kid string) ([]byte, error) {
	b, err := json.Marshal(wireLine{
		Seq: r.Seq, At: r.At.UTC(), Kind: r.Kind,
		Subject: entityJSON(r.Subject), Object: entityJSON(r.Object),
		Detail: r.Detail, Epoch: r.Epoch, RestoreSeq: r.RestoreSeq, Source: r.Source, KID: kid,
	})
	if err != nil {
		return nil, fmt.Errorf("journal: encode line %d: %w", r.Seq, err)
	}
	return b, nil
}

// canonicalHeader is the one encoding of the header before its mac.
func canonicalHeader(h Header) ([]byte, error) {
	b, err := json.Marshal(wireHeader{
		Seq: 0, InstallID: h.InstallID, App: h.App, StartedAt: h.StartedAt.UTC(),
		KID: h.KID, BaseSeq: h.BaseSeq, BaseMAC: h.BaseMAC,
	})
	if err != nil {
		return nil, fmt.Errorf("journal: encode header: %w", err)
	}
	return b, nil
}

// keyFor derives the MAC key of kid.
func keyFor(keys Keys, kid string) ([]byte, error) {
	key, err := keys.DeriveKeyFor(kid, macInfo)
	if err != nil {
		return nil, fmt.Errorf("journal: derive key %q: %w", kid, err)
	}
	if len(key) == 0 {
		return nil, fmt.Errorf("journal: derive key %q: empty key", kid)
	}
	return key, nil
}

// sealHeader signs h under its own KID, chaining from h.BaseMAC, and returns
// the stored line and the header with its mac set.
func sealHeader(keys Keys, h Header) ([]byte, Header, error) {
	key, err := keyFor(keys, h.KID)
	if err != nil {
		return nil, h, err
	}
	body, err := canonicalHeader(h)
	if err != nil {
		return nil, h, err
	}
	h.MAC = sign(key, h.BaseMAC, body)
	return seal(body, h.MAC), h, nil
}

// sealLine signs r (numbered r.Seq) under the active derive key, chaining
// from prevMAC, and returns the stored line and its mac.
func sealLine(keys Keys, prevMAC string, r Revocation) (line []byte, mac string, err error) {
	kid := keys.ActiveDeriveID()
	key, err := keyFor(keys, kid)
	if err != nil {
		return nil, "", err
	}
	body, err := canonicalLine(r, kid)
	if err != nil {
		return nil, "", err
	}
	mac = sign(key, prevMAC, body)
	return seal(body, mac), mac, nil
}

// decodeSeq reads a line's seq, which every line (the header included) has,
// without checking anything else.
func decodeSeq(line []byte) (uint64, error) {
	var v struct {
		Seq *uint64 `json:"seq"`
	}
	if err := json.Unmarshal(line, &v); err != nil {
		return 0, err
	}
	if v.Seq == nil {
		return 0, errors.New("no seq")
	}
	return *v.Seq, nil
}

// decodeLine reads a revocation line's canonical bytes back.
func decodeLine(canonical []byte) (Revocation, string, error) {
	var w wireLine
	if err := json.Unmarshal(canonical, &w); err != nil {
		return Revocation{}, "", err
	}
	return Revocation{Seq: w.Seq, At: w.At, Kind: w.Kind, Subject: w.Subject.ref(), Object: w.Object.ref(),
		Detail: w.Detail, Epoch: w.Epoch, RestoreSeq: w.RestoreSeq, Source: w.Source}, w.KID, nil
}

// decodeHeader reads the header's canonical bytes back.
func decodeHeader(canonical []byte) (Header, error) {
	var w wireHeader
	if err := json.Unmarshal(canonical, &w); err != nil {
		return Header{}, err
	}
	if w.Seq != 0 {
		return Header{}, fmt.Errorf("the first line has seq %d, want the header (seq 0)", w.Seq)
	}
	if w.InstallID == "" || w.KID == "" {
		return Header{}, errors.New("the first line is not a header (no install_id or kid)")
	}
	return Header{InstallID: w.InstallID, App: w.App, StartedAt: w.StartedAt, KID: w.KID, BaseSeq: w.BaseSeq, BaseMAC: w.BaseMAC}, nil
}

// IsRevocation reports whether a line of this kind is a revocation. gap,
// restore and restore_aborted lines are bookkeeping and never count in a
// coverage comparison (§5B.3.6 "Boot re-apply" (1), SEC4-5); a kind this
// build does not know counts, so a newer build's line is never skipped.
func IsRevocation(k Kind) bool {
	switch k {
	case Gap, Restore, RestoreAborted:
		return false
	}
	return true
}
