// file: internal/books/cutover/m3/rewrite_test.go
// version: 1.0.0
// guid: de19968f-fbff-4b13-a3b2-b6791e141926
// last-edited: 2026-11-12

package m3

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// journalWith builds a merge journal the way internal/usermerge writes one: an
// invite row's key image is its raw token, the allow-list row's image holds
// the token it was granted by, and a conflict's text names a key.
func journalWith(status, tok, allowTok string) []byte {
	image := func(v any) string {
		raw, _ := json.Marshal(v)
		return base64.StdEncoding.EncodeToString(raw)
	}
	doc := map[string]any{
		"id": "01M7ZZZJOURNAL0000000001A", "status": status, "digest": "d1g3st",
		"survivor_id": "01M7ZZZSURVIVOR000000001B", "absorbed_id": "01M7ZZZABSORBED000000001C",
		"error": "conflict at invite:" + tok,
		"ops": []any{
			map[string]any{"key": "invite:" + tok, "family": "invites", "reason": "re-owned by the surviving account"},
			map[string]any{"key": "apikey:01M7ZZZKEY0000000000001D", "family": "api_keys", "reason": "re-linked"},
			map[string]any{"key": "idx:invite:username:two.face", "family": "invites", "before": image(tok), "after": image(tok)},
			map[string]any{"key": "inviteallow:harvey@arkham.invalid", "family": "invite_allow",
				"before": image(map[string]any{"user_id": "01M7ZZZABSORBED000000001C", "invite_token": allowTok}),
				"after":  image(map[string]any{"user_id": "01M7ZZZSURVIVOR000000001B", "invite_token": allowTok})},
		},
	}
	raw, _ := json.Marshal(doc)
	return raw
}

// TestRewriteJournal_ReplacesEveryRawToken: a journal's raw invite token is
// replaced by invite-sha256:<hex of its SHA-256> in the key image, in the base64
// values of the images and in the conflict text, and in no place does it stay.
func TestRewriteJournal_ReplacesEveryRawToken(t *testing.T) {
	const tok, allowTok = "mck-riddler-invite-0001", "mck-owlman-allow-0002"
	raw := journalWith("applied", tok, allowTok)
	out, changed, err := rewriteJournal(raw)
	if err != nil || !changed {
		t.Fatalf("rewriteJournal: changed %v, err %v", changed, err)
	}
	for _, raw := range []string{tok, allowTok} {
		if bytes.Contains(out, []byte(raw)) {
			t.Errorf("the raw token %q is still in the journal", raw)
		}
		// The base64 form of a value holding the token is gone too.
		for _, v := range []string{base64.StdEncoding.EncodeToString([]byte(raw))} {
			if bytes.Contains(out, []byte(v)) {
				t.Errorf("a base64 image of %q is still in the journal", raw)
			}
		}
	}
	if want := tokenImage(tok); !bytes.Contains(out, []byte(want)) || !strings.HasPrefix(want, "invite-sha256:") || len(want) != len("invite-sha256:")+64 {
		t.Errorf("the journal lacks the invite's image %s", want)
	}
	// The allow-list token sits in the base64 images of its row: decode them.
	j, err := decodeJournal(out)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, op := range j.ops {
		if key, _ := stringMember(op, "key"); key != "inviteallow:harvey@arkham.invalid" {
			continue
		}
		for _, name := range []string{"before", "after"} {
			v, ok := bytesMember(op, name)
			if !ok || !bytes.Contains(v, []byte(tokenImage(allowTok))) {
				t.Errorf("the allow-list op's %s image lacks the token's image: %s", name, v)
			}
			found++
		}
	}
	if found != 2 {
		t.Errorf("found %d allow-list images, want 2", found)
	}
	// Everything else is kept: the digest, the status, the other ops.
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	if string(doc["digest"]) != `"d1g3st"` || string(doc["status"]) != `"applied"` {
		t.Errorf("the rewrite changed the digest or the status: %s %s", doc["digest"], doc["status"])
	}
	if !bytes.Contains(out, []byte("apikey:01M7ZZZKEY0000000000001D")) {
		t.Errorf("an op that names no token lost its key")
	}
}

// TestRewriteJournal_Idempotent: the rewrite of a rewritten journal changes
// nothing and returns it as it is, so M3 may rewrite the journals at every
// chunk's first item without ever hashing a digest again.
func TestRewriteJournal_Idempotent(t *testing.T) {
	once, changed, err := rewriteJournal(journalWith("undone", "mck-bane-invite-0003", "mck-ra-allow-0004"))
	if err != nil || !changed {
		t.Fatalf("first pass: changed %v, err %v", changed, err)
	}
	twice, changed, err := rewriteJournal(once)
	if err != nil {
		t.Fatal(err)
	}
	if changed || !bytes.Equal(once, twice) {
		t.Errorf("a second pass changed the journal (changed %v)", changed)
	}
	if again, _, _ := rewriteJournal(twice); !bytes.Equal(again, once) {
		t.Errorf("a third pass changed the journal")
	}
}

// TestRewriteJournal_NoTokenIsUntouched: a journal that names no token comes
// back as the same bytes, unchanged, so nothing is written for it.
func TestRewriteJournal_NoTokenIsUntouched(t *testing.T) {
	raw := []byte(`{"id":"01M7ZZZ","status":"applied","ops":[{"key":"apikey:01M7ZZZ","family":"api_keys"}]}`)
	out, changed, err := rewriteJournal(raw)
	if err != nil || changed || !bytes.Equal(out, raw) {
		t.Errorf("a journal without a token: changed %v, err %v, same bytes %v", changed, err, bytes.Equal(out, raw))
	}
}

// TestRewriteJournal_UndecodableIsAnError: a journal that is not an object is
// an error (the preflight refuses it); the rewrite never guesses.
func TestRewriteJournal_UndecodableIsAnError(t *testing.T) {
	for _, raw := range []string{"", "not json", "[1,2]", `{"status":7}`, `{"ops":"x"}`} {
		if _, _, err := rewriteJournal([]byte(raw)); err == nil {
			t.Errorf("rewriteJournal(%q) succeeded", raw)
		}
	}
}

// TestRewriteJournal_ErrorsNameNoValue: a journal that does not decode is
// reported by a fixed class; the text of a JSON error can quote the row.
func TestRewriteJournal_ErrorsNameNoValue(t *testing.T) {
	_, _, err := rewriteJournal([]byte(`{"status": "mck-secret-hash-value"x}`))
	if err == nil {
		t.Fatal("a malformed journal decoded")
	}
	if strings.Contains(err.Error(), "mck-secret-hash-value") {
		t.Errorf("the error quotes the row: %v", err)
	}
}

// TestJournalTokens_ListsOnlyRawTokens: tokens() names the raw tokens a journal
// still holds and none of the images a rewrite left, which Verify relies on.
func TestJournalTokens_ListsOnlyRawTokens(t *testing.T) {
	raw := journalWith("applied", "mck-scarecrow-invite-0005", "mck-cluemaster-allow-0006")
	j, err := decodeJournal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(j.tokens()); got != 2 {
		t.Errorf("a fresh journal names %d tokens, want 2", got)
	}
	out, _, _ := rewriteJournal(raw)
	j, err = decodeJournal(out)
	if err != nil {
		t.Fatal(err)
	}
	if got := j.tokens(); len(got) != 0 {
		t.Errorf("a rewritten journal still names %d raw tokens", len(got))
	}
}
