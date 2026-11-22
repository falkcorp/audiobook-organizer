// file: internal/books/cutover/m6/group.go
// version: 1.0.2
// guid: 9a7f3c52-6e0d-4d89-bb54-0e8f4b1d2c77
// last-edited: 2026-11-19

package m6

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/storage"
)

// headSetting is HEAD's settings row (the blob's envelope), frozen here: the
// config JSON is the string in Value.
type headSetting struct {
	Key      string `json:"key"`
	Value    string `json:"value"`
	Type     string `json:"type"`
	IsSecret bool   `json:"is_secret"`
}

// consumedMembers are the top-level blob members M6 reads. Any other member that
// still holds something is kept, raw, in quarantine, so the blob row's deletion
// drops nothing no part of the step read.
var consumedMembers = map[string]bool{
	"root_dir": true, "path_aliases": true, "metadata_sources": true, "openai_base_url": true,
	"ai_endpoints": true, "protected_paths": true, "download_client": true, "plugins": true,
	"deluge_web_url": true, "itunes": true,
}

// groupRun is one pass over the config group: the blob, every import_path: row
// and the counter, read and deleted together in one chunk.
type groupRun struct {
	m     *Migration
	c     *chunk
	top   map[string]json.RawMessage
	set   secretSet
	done  map[string]bool           // blob members already quarantined
	wrote map[integrations.Ref]bool // instances imported this pass
	seen  map[integrations.Ref]string
	// exact is, for the list being converted, each instance ID some entry
	// already has as its HEAD ID, with that entry's member: the ID it wins
	// whatever the order (exactIDs), unless it is not written (settleDeferred).
	exact map[integrations.Ref]string
	// deferred is the renamed entries of the list whose new ID is another
	// entry's exact HEAD ID, decided once the list is done (settleDeferred).
	deferred []*pending
	// openAIKeyUsed: a written AI instance refers to, or copies, the stored
	// OpenAI key.
	openAIKeyUsed bool
}

func (g *groupRun) quarantineMember(member string, raw []byte, reason string) error {
	return g.m.quarantineMember(g.c, member, raw, reason, g.done)
}

// instance imports p, refusing a second instance under an ID this pass already
// wrote (two sources whose IDs collide after the rename): the later one is
// quarantined, never written over the first. A renamed entry whose new ID is
// another entry's exact HEAD ID waits for the end of the list (settleDeferred).
func (g *groupRun) instance(ctx context.Context, p *pending) error {
	if owner, taken := g.exact[p.inst.Config.Ref]; taken && p.renamedFrom != "" && owner != p.member {
		g.deferred = append(g.deferred, p)
		return nil
	}
	return g.write(ctx, p)
}

func (g *groupRun) write(ctx context.Context, p *pending) error {
	ref := p.inst.Config.Ref
	if first, dup := g.seen[ref]; dup {
		g.m.report(Entry{Kind: "instance", ID: ref.String(), Detail: "id already taken by " + first})
		return g.quarantineMember(p.member, p.raw, reasonInstanceConflict)
	}
	g.seen[ref] = p.member
	wrote, err := g.m.writeInstance(ctx, g.c, p, g.done)
	if err != nil {
		return err
	}
	if wrote {
		g.wrote[ref] = true
		g.openAIKeyUsed = g.openAIKeyUsed || p.usesOpenAIKey
	}
	return nil
}

// settleDeferred decides the renamed entries that landed on another entry's
// exact HEAD ID, once every entry of the list has been tried. The entry whose
// HEAD ID it is keeps the ID, listed before or after, when it was written: the
// renamed one is the duplicate (and must not take that entry's credentials,
// which M2 stored under this very name). When that entry was not written (it
// did not decode, or core refused it or holds another instance under the ID),
// nothing holds the ID and the first renamed entry takes it. It still takes
// no credential M2 stored under the new name, which belongs to that entry
// (secretRefs reports each), except the one aiInstance binds on purpose: HEAD's
// OpenAI key, for a row whose auth_ref sent it that key.
func (g *groupRun) settleDeferred(ctx context.Context) error {
	for _, p := range g.deferred {
		ref := p.inst.Config.Ref
		owner := g.exact[ref]
		if g.wrote[ref] {
			// The holder is the exact entry, or an earlier renamed entry that took the
			// ID because the exact one was not written (r2 m-b).
			detail := "id already taken by " + g.seen[ref]
			if g.seen[ref] == owner {
				detail += ", whose HEAD id it is"
			}
			g.m.report(Entry{Kind: "instance", ID: ref.String(), Detail: detail})
			if err := g.quarantineMember(p.member, p.raw, reasonInstanceConflict); err != nil {
				return err
			}
			continue
		}
		if g.seen[ref] == owner {
			delete(g.seen, ref)
		}
		if _, dup := g.seen[ref]; !dup {
			g.m.report(Entry{Kind: "note", ID: ref.String(), Detail: owner + ", whose HEAD id this is, was not written, so " + p.member + " (HEAD id " + p.renamedFrom + ") takes the id"})
		}
		if err := g.write(ctx, p); err != nil {
			return err
		}
	}
	g.deferred = nil
	return nil
}

// group reads the whole config group, converts it and deletes it.
func (m *Migration) group(ctx context.Context, c *chunk, _ storage.KV) error {
	g := &groupRun{m: m, c: c, done: map[string]bool{}, wrote: map[integrations.Ref]bool{}, seen: map[integrations.Ref]string{}}
	blob, err := m.get(ctx, KeyBlob)
	if err != nil {
		return err
	}
	counter, err := m.get(ctx, KeyImportCounter)
	if err != nil {
		return err
	}
	rows, index, err := m.importPathRows(ctx)
	if err != nil {
		return err
	}
	g.set, err = m.names(ctx)
	if err != nil {
		return err
	}

	blobOK := false
	if blob != nil {
		var hs headSetting
		if json.Unmarshal(blob, &hs) == nil && json.Unmarshal([]byte(hs.Value), &g.top) == nil && g.top != nil {
			blobOK = true
		} else if err := c.quarantine(KeyBlob, blob, reasonBlobUndecodable); err != nil {
			return err
		}
	}
	if err := g.roots(ctx, rows, index, counter != nil); err != nil {
		return err
	}
	if blobOK {
		if err := g.instances(ctx); err != nil {
			return err
		}
		if err := g.webhooks(ctx); err != nil {
			return err
		}
		if err := g.leftovers(); err != nil {
			return err
		}
		return c.consume(KeyBlob, OutcomeBlob)
	}
	return nil
}

// importPathRows reads the group's import_path: rows from the legacy range,
// split into the rows proper and the path index rows.
func (m *Migration) importPathRows(ctx context.Context) (rows, index []storage.KV, err error) {
	start := []byte(PrefixImportPath)
	end := []byte(PrefixImportPath)
	end[len(end)-1]++
	for kv, serr := range m.cfg.Legacy().Scan(ctx, storage.ScanOptions{Start: start, End: end}) {
		if serr != nil {
			return nil, nil, fmt.Errorf("m6: scan import_path: %w", serr)
		}
		kv = kv.Clone()
		m.read(string(kv.Key))
		if bytes.HasPrefix(kv.Key, []byte(PrefixImportPath+"path:")) {
			index = append(index, kv)
		} else {
			rows = append(rows, kv)
		}
	}
	return rows, index, nil
}

// exactIDs maps each instance ID that an entry of list already has as its HEAD
// ID (kept by instanceID, not renamed) to that entry's member; the first such
// entry when two share one (the later is the ordinary duplicate).
func exactIDs(kind integrations.Kind, list string, entries []json.RawMessage) map[integrations.Ref]string {
	out := map[integrations.Ref]string{}
	for i, er := range entries {
		var e struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(er, &e) != nil {
			continue
		}
		id, renamed, ok := instanceID(e.ID)
		if !ok || renamed {
			continue
		}
		ref := integrations.Ref{Kind: kind, Instance: id}
		if _, dup := out[ref]; !dup {
			out[ref] = fmt.Sprintf("%s.%d", list, i)
		}
	}
	return out
}

// instances converts the metadata sources, the AI endpoints (and the synthesised
// ai/openai) and Deluge.
func (g *groupRun) instances(ctx context.Context) error {
	m := g.m
	// metadata_sources
	if raw, ok := g.top["metadata_sources"]; ok {
		var entries []json.RawMessage
		if err := json.Unmarshal(raw, &entries); err != nil {
			if err := g.quarantineMember("metadata_sources", raw, reasonBlobUndecodable); err != nil {
				return err
			}
		}
		g.exact = exactIDs(integrations.KindMetadata, "metadata_sources", entries)
		for i, er := range entries {
			p, bad, why := m.metadataInstance(er, i, g.set)
			if p == nil {
				if err := g.quarantineMember(bad, er, why); err != nil {
					return err
				}
				continue
			}
			if err := g.instance(ctx, p); err != nil {
				return err
			}
		}
		if err := g.settleDeferred(ctx); err != nil {
			return err
		}
	}
	// ai_endpoints
	haveOpenAI := false
	if raw, ok := g.top["ai_endpoints"]; ok {
		var entries []json.RawMessage
		if err := json.Unmarshal(raw, &entries); err != nil {
			if err := g.quarantineMember("ai_endpoints", raw, reasonBlobUndecodable); err != nil {
				return err
			}
		}
		g.exact = exactIDs(integrations.KindAI, "ai_endpoints", entries)
		for i, er := range entries {
			p, bad, why := m.aiInstance(er, i, g.set)
			if p == nil {
				if err := g.quarantineMember(bad, er, why); err != nil {
					return err
				}
				continue
			}
			// Decided by the new ID: a row renamed to openai is the row HEAD's
			// synthesis would collide with, and a second ai/openai is a duplicate.
			if p.inst.Config.Ref.Instance == instanceOpenAI {
				haveOpenAI = true
			}
			if err := g.instance(ctx, p); err != nil {
				return err
			}
		}
		if err := g.settleDeferred(ctx); err != nil {
			return err
		}
	}
	g.exact = nil
	// The row standing for HEAD's legacy OpenAI path, synthesised when an OpenAI
	// key is stored and no row has the ID openai. HEAD's own synthesis ran only
	// while ai_endpoints was absent (internal/config/ai_endpoints.go
	// migrateAIEndpointsBlob), but whatever ai_endpoints_routing said, the sites
	// the pool never routed (legacyKeyCapabilities) read the key from the scalars
	// and ran only while enable_ai_parsing was on. So the row is ticked for those
	// capabilities and is enabled exactly when enable_ai_parsing is: written
	// tickless and enabled it health-probed with the key and served nothing even
	// when AI was off (r2 m-a).
	if !haveOpenAI && g.set[secretOpenAI] {
		var base string
		if raw, ok := g.top["openai_base_url"]; ok {
			if err := json.Unmarshal(raw, &base); err != nil {
				if err := g.quarantineMember("openai_base_url", raw, reasonBlobUndecodable); err != nil {
					return err
				}
			}
		}
		aiParsing, err := m.cfg.EnableAIParsing(ctx)
		if err != nil {
			return fmt.Errorf("m6: read enable_ai_parsing: %w", err)
		}
		p, proven := m.openAIInstance(base, g.set, aiParsing)
		switch {
		case !proven:
			if err := g.quarantineMember("openai_base_url", []byte(base), reasonUnprovenURL); err != nil {
				return err
			}
		default:
			if err := g.instance(ctx, p); err != nil {
				return err
			}
		}
	}
	if g.set[secretOpenAI] && !g.openAIKeyUsed {
		// A row renamed to openai with no auth_ref suppresses the synthesis and
		// takes no ref under its new name (secretRefs): HEAD's key then reaches
		// no instance. Said, never silent.
		g.m.report(Entry{Kind: "note", ID: integrations.Ref{Kind: integrations.KindAI, Instance: instanceOpenAI}.String(), Detail: "the stored OpenAI key " + string(secretOpenAI) + " is referred to by no migrated AI endpoint: it stays in the store, unreferenced; bind it to an endpoint to use it"})
	}
	// download_client.torrent daemon fields and deluge_web_url
	if err := g.deluge(ctx); err != nil {
		return err
	}
	return nil
}

// deluge converts deluge_web_url to download/deluge. The daemon RPC fields
// (type, host, port, username) HEAD stored beside it have no native field and
// are reported as dropped; the daemon password is in the secrets store already
// and rides on the instance as daemon_password.
//
// The URL is HEAD's effective one, not the blob's: Deluge's web URL is
// environment-or-config-file authoritative at HEAD over the stored blob,
// including a stored empty value (internal/config applyEnvAuthoritativeConfig),
// so on an install that pins it that way the blob's is empty or stale. Config.
// Effective answers the operator's URL for download/deluge, and the blob's is
// the fallback. With neither there is no destination and no instance, which the
// report says.
func (g *groupRun) deluge(ctx context.Context) error {
	m := g.m
	ref := integrations.Ref{Kind: integrations.KindDownload, Instance: instanceDeluge}
	eff, err := m.cfg.Effective(ctx, ref)
	if err != nil {
		return fmt.Errorf("m6: the effective destinations of %s: %w", ref, err)
	}
	operatorURL := strings.TrimSpace(eff["url"])

	var web string
	blobDecodes := true
	if raw, ok := g.top["deluge_web_url"]; ok {
		if err := json.Unmarshal(raw, &web); err != nil {
			blobDecodes = false
			web = ""
			if err := g.quarantineMember("deluge_web_url", raw, reasonBlobUndecodable); err != nil {
				return err
			}
		}
	}
	// The operator's URL is used whether or not the blob's member decodes: HEAD
	// ignored the stored value when one was supplied.
	use, fromOperator := web, false
	if operatorURL != "" {
		use, fromOperator = operatorURL, true
		if strings.TrimSpace(web) != "" && web != operatorURL {
			// HEAD ignored the stored value; so does the migrated instance. Said,
			// without either URL.
			m.report(Entry{Kind: "note", ID: ref.String(), Detail: "the stored deluge_web_url is superseded by the operator's (environment or config file) and is not carried"})
		}
	}
	p, proven := m.delugeInstance(use, web, g.set)
	switch {
	case !proven && fromOperator:
		// The URL is the operator's, not a row M6 owns: reported, not quarantined.
		// The blob's own value is quarantined only when it is that same URL.
		m.report(Entry{Kind: "note", ID: ref.String(), Detail: "a credential cannot be ruled out of the operator's (environment or config file) Deluge web URL: no download/deluge instance is created; supply the URL without userinfo"})
		if blobDecodes && web == operatorURL {
			if err := g.quarantineMember("deluge_web_url", []byte(web), reasonUnprovenURL); err != nil {
				return err
			}
		}
	case !proven:
		if err := g.quarantineMember("deluge_web_url", []byte(web), reasonUnprovenURL); err != nil {
			return err
		}
	case p != nil:
		if err := g.instance(ctx, p); err != nil {
			return err
		}
	default:
		m.report(Entry{Kind: "note", ID: ref.String(), Detail: "no web URL is stored and none is supplied by the environment or config file: no download/deluge instance is created"})
	}
	raw, ok := g.top["download_client"]
	if !ok {
		return nil
	}
	var dc struct {
		Torrent struct {
			Type   string `json:"type"`
			Deluge struct {
				Host     string `json:"host"`
				Port     int    `json:"port"`
				Username string `json:"username"`
			} `json:"deluge"`
		} `json:"torrent"`
	}
	if err := json.Unmarshal(raw, &dc); err != nil {
		return g.quarantineMember("download_client", raw, reasonBlobUndecodable)
	}
	d := dc.Torrent
	if d.Type == "" && d.Deluge.Host == "" && d.Deluge.Port == 0 && d.Deluge.Username == "" {
		return nil
	}
	// Reported, not quarantined: the native Web UI connects to its daemon with
	// the host entry it already stores (core/integrations/download/deluge
	// Settings), so these fields are superseded rather than unreadable, and the
	// corpus' quarantine report is exactly the rows that failed to import.
	g.m.report(Entry{Kind: "note", ID: "download/deluge", Detail: "download_client.torrent daemon fields (type, host, port, username) are not carried: the native Web UI uses the host entry it stores"})
	return nil
}

// leftovers quarantines every top-level blob member no part of M6 reads that
// still holds a value.
func (g *groupRun) leftovers() error {
	names := make([]string, 0, len(g.top))
	for name := range g.top {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if consumedMembers[name] || emptyJSON(g.top[name]) {
			continue
		}
		if err := g.quarantineMember(name, g.top[name], reasonUnreadMember); err != nil {
			return err
		}
	}
	return nil
}

// emptyJSON reports a value that holds nothing: null, "", an empty object or
// array, or a container of only such values.
func emptyJSON(raw json.RawMessage) bool {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return false
	}
	return emptyValue(v)
}

func emptyValue(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case []any:
		for _, e := range t {
			if !emptyValue(e) {
				return false
			}
		}
		return true
	case map[string]any:
		for _, e := range t {
			if !emptyValue(e) {
				return false
			}
		}
		return true
	}
	return false
}
