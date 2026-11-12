// file: internal/books/cutover/identity_journal_test.go
// version: 1.0.0
// guid: 678a0edd-6e0d-4c9a-bc80-1c9d641da9ee
// last-edited: 2026-11-12

package cutover

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m3"
)

// tokenImageOf is what a raw token becomes in a rewritten journal.
func tokenImageOf(tok string) string { return "invite-sha256:" + idDigest(tok) }

// TestIdentityCutover_JournalTokensHashedOnce: M3 reads the merge journals at
// the first item of every chunk, so a journal rewritten by chunk 1 is read
// again by chunk 2. A token in a value (the allow-list row's invite_token in a
// base64 image) is hashed once however many chunks the step takes, never a
// digest of a digest; a journal that names no token is never written; and the
// outcome does not depend on the chunk size.
func TestIdentityCutover_JournalTokensHashedOnce(t *testing.T) {
	const (
		tok      = "mck-joker-invite-0001"
		allowTok = "mck-penguin-allow-0002"
	)
	image := func(v any) string {
		raw, _ := json.Marshal(v)
		return base64.StdEncoding.EncodeToString(raw)
	}
	withToken := mustMarshal(map[string]any{
		"id": "01M7ZZZJOURNALTOKEN00001A", "status": "applied", "digest": "d1g3st", "error": "conflict at invite:" + tok,
		"survivor_id": uBarbara, "absorbed_id": uDick,
		"ops": []any{
			map[string]any{"key": "invite:" + tok, "family": "invites", "reason": "re-owned"},
			map[string]any{"key": "inviteallow:oswald@iceberg.invalid", "family": "invite_allow",
				"before": image(map[string]any{"user_id": uDick, "invite_token": allowTok}),
				"after":  image(map[string]any{"user_id": uBarbara, "invite_token": allowTok})},
		},
	})
	noToken := mustMarshal(map[string]any{"id": "01M7ZZZJOURNALCLEAN00002B", "status": "undone", "ops": []any{
		map[string]any{"key": "apikey:" + kScopedView, "family": "api_keys", "reason": "re-linked"}}})
	rows := idRows(
		seedRow{"usermerge:journal:01M7ZZZJOURNALTOKEN00001A", withToken},
		seedRow{"usermerge:journal:01M7ZZZJOURNALCLEAN00002B", noToken},
	)
	var outs [2]map[string]string
	for i, chunk := range []int{1, 1000} {
		r := newIDRig(t, "stage1", rows, func(r *idRig) { r.chunkItems = chunk })
		snap, err := r.run(t)
		if err != nil {
			t.Fatalf("chunk size %d: %v\n%s", chunk, err, r.logs.String())
		}
		outs[i] = map[string]string{}
		for k, v := range snap.Legacy {
			if len(k) > len("usermerge:journal:") && k[:len("usermerge:journal:")] == "usermerge:journal:" {
				outs[i][k] = string(v)
			}
		}
		// One journal names a token, and it is rewritten once however many chunks
		// the step takes; the one that names none is never written.
		if n := strings.Count(r.logs.String(), m3.LogJournalRewritten); n != 1 {
			t.Errorf("chunk size %d: %d journals were rewritten, want exactly the one that names a token\n%s", chunk, n, r.logs.String())
		}
		got := []byte(outs[i]["usermerge:journal:01M7ZZZJOURNALTOKEN00001A"])
		if len(got) == 0 {
			t.Fatalf("chunk size %d: the journal is gone", chunk)
		}
		for _, raw := range []string{tok, allowTok} {
			if bytes.Contains(got, []byte(raw)) {
				t.Errorf("chunk size %d: the raw token %q is still in the journal", chunk, raw)
			}
		}
		if !bytes.Contains(got, []byte(tokenImageOf(tok))) {
			t.Errorf("chunk size %d: the invite's image is missing", chunk)
		}
		if bytes.Contains(got, []byte(tokenImageOf(tokenImageOf(tok)))) || bytes.Contains(got, []byte(idDigest(idDigest(tok)))) {
			t.Errorf("chunk size %d: a digest was hashed again", chunk)
		}
		var doc struct {
			Ops []map[string]json.RawMessage `json:"ops"`
		}
		if err := json.Unmarshal(got, &doc); err != nil {
			t.Fatal(err)
		}
		for _, op := range doc.Ops {
			var key string
			_ = json.Unmarshal(op["key"], &key)
			if key != "inviteallow:oswald@iceberg.invalid" {
				continue
			}
			var after []byte
			if err := json.Unmarshal(op["after"], &after); err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(after, []byte(tokenImageOf(allowTok))) || bytes.Contains(after, []byte(tokenImageOf(tokenImageOf(allowTok)))) {
				t.Errorf("chunk size %d: the allow-list image holds %s, want the token hashed exactly once", chunk, after)
			}
		}
		if got := outs[i]["usermerge:journal:01M7ZZZJOURNALCLEAN00002B"]; got != string(noToken) {
			t.Errorf("chunk size %d: a journal naming no token was rewritten: %s", chunk, got)
		}
	}
	for k, v := range outs[0] {
		if outs[1][k] != v {
			t.Errorf("journal %s depends on the chunk size", k)
		}
	}
}
