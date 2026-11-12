// file: internal/books/cutover/m3/rewrite.go
// version: 1.0.0
// guid: 21647e7f-8845-41b7-a0a1-e13cda9c0d6b
// last-edited: 2026-11-12

package m3

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// Merge journals are books' rows (usermerge:journal:), kept read-only after
// the cutover (05-authn §11 "Merges at the cutover"). Their ops carry the key
// images of the rows a merge moved, and an invite row's key is its raw token:
// a bearer secret the cutover must not leave in the live store. M3 rewrites
// each journal once, replacing every raw token with its SHA-256.
//
// A key image invite:<token> becomes invite-sha256:<hex>, a form that does not
// start with "invite:", so a second pass finds nothing to rewrite and writes
// nothing. The token also sits in the images' values (the Invite row's
// "token", an idx:invite:username: entry's value, an inviteallow: row's
// "invite_token") and in the journal's error text (a conflict names its key),
// so every occurrence of a token the journal names is replaced, not only the
// key image. The journal's digest (a hash of the before and after images) no
// longer matches the rewritten images; no merge is undone after the cutover,
// so nothing reads it.

// tokenImagePrefix is the key image of an invite whose token was hashed.
const tokenImagePrefix = "invite-sha256:"

// tokenImage is what replaces a raw token wherever a journal names it:
// "invite-sha256:" and the hex SHA-256 of the token. The prefix is what makes
// the rewrite idempotent: an image is recognised and never hashed again, and
// it is not an "invite:" key, so Verify's check for a raw token passes it.
func tokenImage(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return tokenImagePrefix + hex.EncodeToString(sum[:])
}

// journalDoc is a journal with only the members this package reads decoded.
type journalDoc struct {
	status string
	ops    []map[string]json.RawMessage
	doc    map[string]json.RawMessage
}

func decodeJournal(raw []byte) (*journalDoc, error) {
	j := &journalDoc{}
	if err := decodeJSON(raw, &j.doc); err != nil {
		return nil, err
	}
	if s, ok := j.doc["status"]; ok {
		if err := json.Unmarshal(s, &j.status); err != nil {
			return nil, errBadShape
		}
	}
	if o, ok := j.doc["ops"]; ok && string(o) != "null" {
		if err := json.Unmarshal(o, &j.ops); err != nil {
			return nil, errBadShape
		}
	}
	return j, nil
}

// stringMember decodes op[name] as a string, "" when absent.
func stringMember(op map[string]json.RawMessage, name string) (string, bool) {
	raw, ok := op[name]
	if !ok {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

// bytesMember decodes op[name] as the base64 []byte a UserMergeOp encodes.
func bytesMember(op map[string]json.RawMessage, name string) ([]byte, bool) {
	raw, ok := op[name]
	if !ok || string(raw) == "null" {
		return nil, false
	}
	var b []byte
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, false
	}
	return b, true
}

// journalTokens are the raw tokens a journal names: the key image of an invite
// row, and the invite_token of an inviteallow: row's image.
func (j *journalDoc) tokens() map[string]string {
	toks := map[string]string{}
	for _, op := range j.ops {
		key, _ := stringMember(op, "key")
		if tok, ok := strings.CutPrefix(key, prefixInvite); ok && tok != "" {
			toks[tok] = tokenImage(tok)
		}
		if strings.HasPrefix(key, prefixInviteAllow) {
			for _, name := range []string{"before", "after"} {
				v, ok := bytesMember(op, name)
				if !ok {
					continue
				}
				var g struct {
					Token string `json:"invite_token"`
				}
				// A token this step already replaced is its image, not a raw
				// token: hashing it again would make the rewrite change a
				// journal every chunk and never settle.
				if json.Unmarshal(v, &g) == nil && g.Token != "" && !strings.HasPrefix(g.Token, tokenImagePrefix) {
					toks[g.Token] = tokenImage(g.Token)
				}
			}
		}
	}
	return toks
}

// rewriteJournal returns raw with every raw invite token replaced by its
// SHA-256, and whether anything changed. A journal that names no token comes
// back as it is.
func rewriteJournal(raw []byte) ([]byte, bool, error) {
	j, err := decodeJournal(raw)
	if err != nil {
		return nil, false, err
	}
	toks := j.tokens()
	if len(toks) == 0 {
		return raw, false, nil
	}
	replace := func(b []byte) []byte {
		for tok, dig := range toks {
			b = bytes.ReplaceAll(b, []byte(tok), []byte(dig))
		}
		return b
	}
	for _, op := range j.ops {
		if key, ok := stringMember(op, "key"); ok {
			if tok, isInvite := strings.CutPrefix(key, prefixInvite); isInvite && tok != "" {
				enc, err := json.Marshal(toks[tok])
				if err != nil {
					return nil, false, errBadShape
				}
				op["key"] = enc
			}
		}
		for _, name := range []string{"before", "after"} {
			v, ok := bytesMember(op, name)
			if !ok {
				continue
			}
			enc, err := json.Marshal(replace(v))
			if err != nil {
				return nil, false, errBadShape
			}
			op[name] = enc
		}
	}
	if _, ok := j.doc["ops"]; ok {
		ops, err := json.Marshal(j.ops)
		if err != nil {
			return nil, false, errBadShape
		}
		j.doc["ops"] = ops
	}
	out, err := json.Marshal(j.doc)
	if err != nil {
		return nil, false, errBadShape
	}
	// The error text and warnings of a journal name the key of a conflict.
	out = replace(out)
	return out, !bytes.Equal(out, raw), nil
}
