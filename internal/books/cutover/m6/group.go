// file: internal/books/cutover/m6/group.go
// version: 1.0.0
// guid: 9a7f3c52-6e0d-4d89-bb54-0e8f4b1d2c77
// last-edited: 2026-11-19

package m6

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"

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
}

func (g *groupRun) quarantineMember(member string, raw []byte, reason string) error {
	return g.m.quarantineMember(g.c, member, raw, reason, g.done)
}

// instance imports p, refusing a second instance under an ID this pass already
// wrote (two sources whose IDs collide after the rename): the later one is
// quarantined, never written over the first.
func (g *groupRun) instance(ctx context.Context, p *pending) error {
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
	}
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
		if bytes.HasPrefix(kv.Key, []byte(PrefixImportPath+"path:")) {
			index = append(index, kv)
		} else {
			rows = append(rows, kv)
		}
	}
	return rows, index, nil
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
		for i, er := range entries {
			p, bad := m.metadataInstance(er, i, g.set)
			if p == nil {
				if err := g.quarantineMember(bad, er, reasonBlobUndecodable); err != nil {
					return err
				}
				continue
			}
			if err := g.instance(ctx, p); err != nil {
				return err
			}
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
		for i, er := range entries {
			p, bad := m.aiInstance(er, i, g.set)
			if p == nil {
				if err := g.quarantineMember(bad, er, reasonBlobUndecodable); err != nil {
					return err
				}
				continue
			}
			if p.inst.Config.Ref.Instance == instanceOpenAI {
				haveOpenAI = true
			}
			if err := g.instance(ctx, p); err != nil {
				return err
			}
		}
	}
	// The row HEAD synthesises when an OpenAI key is stored and no stored row has
	// the ID openai (internal/config/ai_endpoints.go).
	if !haveOpenAI && g.set[secretOpenAI] {
		var base string
		if raw, ok := g.top["openai_base_url"]; ok {
			if err := json.Unmarshal(raw, &base); err != nil {
				if err := g.quarantineMember("openai_base_url", raw, reasonBlobUndecodable); err != nil {
					return err
				}
			}
		}
		if p := m.openAIInstance(base, g.set); p != nil {
			if err := g.instance(ctx, p); err != nil {
				return err
			}
		}
	}
	// download_client.torrent daemon fields and deluge_web_url
	if err := g.deluge(ctx); err != nil {
		return err
	}
	return nil
}

// deluge converts deluge_web_url to download/deluge, and keeps the daemon RPC
// fields (type, host, port, username) HEAD stored beside it, which the native
// instance has no field for, as one quarantine record: the password is in the
// store already, so the record never holds it.
func (g *groupRun) deluge(ctx context.Context) error {
	m := g.m
	if raw, ok := g.top["deluge_web_url"]; ok {
		var web string
		if err := json.Unmarshal(raw, &web); err != nil {
			if err := g.quarantineMember("deluge_web_url", raw, reasonBlobUndecodable); err != nil {
				return err
			}
		} else {
			p, proven := m.delugeInstance(web, g.set)
			switch {
			case !proven:
				if err := g.quarantineMember("deluge_web_url", []byte(web), reasonUnprovenURL); err != nil {
					return err
				}
			case p != nil:
				if err := g.instance(ctx, p); err != nil {
					return err
				}
			}
		}
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
	kept, err := json.Marshal(dc)
	if err != nil {
		return fmt.Errorf("m6: encode the daemon fields: %w", err)
	}
	return g.quarantineMember("download_client.torrent", kept, reasonUnrepresentable)
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
