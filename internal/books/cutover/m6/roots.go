// file: internal/books/cutover/m6/roots.go
// version: 1.0.2
// guid: 5c6b9d2a-5df4-4e1e-9d64-7a0f3a1c1b52
// last-edited: 2026-11-19

package m6

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/library"
	"github.com/falkcorp/mediacore/storage"
)

// The core config paths the roots land in (05-library 5L.10).
const (
	pathRoots     = config.Path("core.library.roots")
	pathMappings  = config.Path("core.library.mappings")
	pathProtected = config.Path("core.library.protected_paths")
)

// Root IDs and the books family M6 writes (05-library 5L.10).
const (
	// BooksLibraryRoot is the entry blob root_dir becomes.
	BooksLibraryRoot library.RootID = "books-library"
	// PrefixRootScan is books' per-root scan state, a family M6 creates.
	PrefixRootScan = "rootscan:"
	// failedDir is the quarantine directory books keeps (HEAD's .failed).
	failedDir = ".failed"
	// itunesSource is the PathMapper source of iTunes mappings.
	itunesSource = "itunes"
)

// RootID is the one pure function between HEAD's import-path ID n and the root
// ID import-<n> (05-library 5L.10, "IDs are kept"), and back.
func RootID(n int) library.RootID { return library.RootID("import-" + strconv.Itoa(n)) }

// ImportPathID is RootID's inverse: n and true for an ID RootID produced.
func ImportPathID(id library.RootID) (int, bool) {
	rest, ok := strings.CutPrefix(string(id), "import-")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(rest)
	if err != nil || n < 0 || strconv.Itoa(n) != rest {
		return 0, false
	}
	return n, true
}

// importPathRow is HEAD's import_path:<n> row (internal/books/store/store.go
// ImportPath), frozen here.
type importPathRow struct {
	ID        int        `json:"id"`
	Path      string     `json:"path"`
	Name      string     `json:"name"`
	Enabled   bool       `json:"enabled"`
	CreatedAt time.Time  `json:"created_at"`
	LastScan  *time.Time `json:"last_scan,omitempty"`
	BookCount int        `json:"book_count"`
}

// RootScan is the row of books' rootscan:<root ID> family: the four HEAD fields
// that are scan state, not library's (05-library 5L.10).
type RootScan struct {
	Enabled   bool       `json:"enabled"`
	CreatedAt time.Time  `json:"created_at"`
	LastScan  *time.Time `json:"last_scan,omitempty"`
	BookCount int        `json:"book_count"`
}

// headPathAlias and headPathMap are HEAD's blob entries (internal/config
// PathAlias, ITunesPathMap), frozen here.
type headPathAlias struct {
	Root    string `json:"root"`
	Windows string `json:"windows"`
	UNC     string `json:"unc"`
	SMBURL  string `json:"smb_url"`
}

type headPathMap struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// plannedRoot is a root being registered and the rows it consumes.
type plannedRoot struct {
	spec library.RootSpec
	scan *RootScan
	key  string // the import_path row, "" for books-library
	// raw is what the root came from, kept for quarantine if core refuses the
	// roots list: the import_path row's value, or root_dir's string. member names
	// root_dir's blob member ("" for an import_path row, which is quarantined by
	// key).
	raw    []byte
	member string
	// aliases are the path_aliases members attached to this root, with their raw
	// JSON, quarantined with it if the list is refused.
	aliases []rawMember
}

// rawMember is a blob member and its raw JSON.
type rawMember struct {
	name string
	raw  []byte
}

// mappingEntry is one resolved iTunes mapping with the blob member it came from.
type mappingEntry struct {
	member string
	raw    []byte
	m      library.Mapping
}

// roots converts the roots, the iTunes mappings and the protected paths of the
// group and stages them through config.Importer. It returns what the rest of the
// pass needs to know: nothing, besides an error.
func (g *groupRun) roots(ctx context.Context, rows, index []storage.KV, counter bool) error {
	m, c := g.m, g.c
	var planned []plannedRoot
	used := map[string]string{} // clean path -> root ID, to refuse a second root at one path

	// books-library from root_dir.
	var rootDir string
	if raw, ok := g.top["root_dir"]; ok {
		if err := json.Unmarshal(raw, &rootDir); err != nil {
			if err := g.quarantineMember("root_dir", raw, reasonBlobUndecodable); err != nil {
				return err
			}
			rootDir = ""
		}
	}
	if strings.TrimSpace(rootDir) != "" {
		if bad := badRootPath(rootDir); bad != "" {
			if err := g.quarantineMember("root_dir", []byte(rootDir), bad); err != nil {
				return err
			}
		} else {
			planned = append(planned, plannedRoot{spec: library.RootSpec{
				ID: BooksLibraryRoot, Path: rootDir, Role: library.RoleLibrary, Owner: Owner,
				Watch: library.WatchOff, Sentinel: library.SentinelBootstrap, MinFree: 0,
				Quarantine: failedDir, AppDir: library.DefaultAppDir,
			}, raw: []byte(rootDir), member: "root_dir"})
			used[filepath.Clean(rootDir)] = string(BooksLibraryRoot)
		}
	}

	// import_path rows, in ID order so a duplicate path always keeps the lower ID.
	type decoded struct {
		n   int
		row importPathRow
		kv  storage.KV
	}
	var ok []decoded
	for _, kv := range rows {
		key := string(kv.Key)
		n, err := strconv.Atoi(strings.TrimPrefix(key, PrefixImportPath))
		var row importPathRow
		switch {
		case err != nil || n < 0 || strconv.Itoa(n) != strings.TrimPrefix(key, PrefixImportPath):
			if err := c.quarantine(key, kv.Value, reasonRootUndecodable); err != nil {
				return err
			}
		case json.Unmarshal(kv.Value, &row) != nil:
			if err := c.quarantine(key, kv.Value, reasonRootUndecodable); err != nil {
				return err
			}
		case row.ID != n:
			if err := c.quarantine(key, kv.Value, reasonRootUndecodable); err != nil {
				return err
			}
		default:
			ok = append(ok, decoded{n: n, row: row, kv: kv})
		}
	}
	sort.SliceStable(ok, func(i, j int) bool { return ok[i].n < ok[j].n })
	for _, d := range ok {
		key := string(d.kv.Key)
		id := RootID(d.n)
		if bad := badRootPath(d.row.Path); bad != "" {
			if err := c.quarantine(key, d.kv.Value, bad); err != nil {
				return err
			}
			continue
		}
		clean := filepath.Clean(d.row.Path)
		if other, dup := used[clean]; dup {
			m.report(Entry{Kind: "root", ID: string(id), Detail: "same path as " + other})
			if err := c.quarantine(key, d.kv.Value, reasonRootConflict); err != nil {
				return err
			}
			continue
		}
		used[clean] = string(id)
		watch := library.WatchOff
		if d.row.Enabled {
			watch = library.WatchAuto
		}
		planned = append(planned, plannedRoot{
			spec: library.RootSpec{
				ID: id, Name: d.row.Name, Path: d.row.Path, Role: library.RoleImport, Owner: Owner,
				Watch: watch, Sentinel: library.SentinelBootstrap, MinFree: library.MinFreeDefault,
			},
			scan: &RootScan{Enabled: d.row.Enabled, CreatedAt: d.row.CreatedAt, LastScan: d.row.LastScan, BookCount: d.row.BookCount},
			key:  key,
			raw:  d.kv.Value,
		})
	}

	if err := g.pathAliases(planned); err != nil {
		return err
	}

	// Core accepts or refuses each of the three lists whole (a *config.FieldError
	// names the list, not a row), and nothing is reported, counted or consumed for
	// a list until core has accepted it. A refused list quarantines every row and
	// member that fed it, so the store stays bootable and nothing is dropped.
	refusedRoots, err := g.stage(pathRoots, rootSpecs(planned), len(planned) > 0)
	if err != nil {
		return err
	}
	var refused []plannedRoot
	if refusedRoots {
		if err := g.quarantineRoots(planned); err != nil {
			return err
		}
		refused, planned = planned, nil
	}

	// Nested roots are registered as the same-owner rule requires and reported.
	for i := range planned {
		for j := range planned {
			if i != j && planned[i].spec.ID != planned[j].spec.ID && library.WithinRoot(planned[i].spec.Path, planned[j].spec.Path) {
				m.report(Entry{Kind: "root", ID: string(planned[i].spec.ID), Detail: "nested inside " + string(planned[j].spec.ID)})
				c.note(OutcomeNestedRoot)
				break
			}
		}
	}

	// itunes.path_mappings, resolved against the planned roots.
	entries, err := g.mappings(planned, refused)
	if err != nil {
		return err
	}
	mappings := make([]library.Mapping, len(entries))
	for i, e := range entries {
		mappings[i] = e.m
	}
	refusedMappings, err := g.stage(pathMappings, mappings, len(mappings) > 0)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if refusedMappings {
			if err := g.quarantineMember(e.member, e.raw, reasonListRefused); err != nil {
				return err
			}
			continue
		}
		m.report(Entry{Kind: "mapping", ID: e.member, Detail: string(e.m.Local.Root)})
		c.note(OutcomeMapping)
	}

	// protected_paths.
	var protected []string
	protectedRaw, hasProtected := g.top["protected_paths"]
	if hasProtected {
		if err := json.Unmarshal(protectedRaw, &protected); err != nil {
			if err := g.quarantineMember("protected_paths", protectedRaw, reasonBlobUndecodable); err != nil {
				return err
			}
			protected = nil
		}
	}
	protected = nonEmpty(protected)
	refusedProtected, err := g.stage(pathProtected, protected, len(protected) > 0)
	if err != nil {
		return err
	}
	if refusedProtected {
		if err := g.quarantineMember("protected_paths", protectedRaw, reasonListRefused); err != nil {
			return err
		}
		protected = nil
	}
	for _, p := range planned {
		m.report(Entry{Kind: "root", ID: string(p.spec.ID), Detail: p.spec.Role.String()})
		if p.scan == nil {
			continue
		}
		val, err := json.Marshal(p.scan)
		if err != nil {
			return fmt.Errorf("m6: encode the scan record of %s: %w", p.spec.ID, err)
		}
		if err := c.w.Set([]byte(PrefixRootScan+string(p.spec.ID)), val); err != nil {
			return err
		}
		if err := c.consume(p.key, OutcomeRoot); err != nil {
			return err
		}
	}
	if len(protected) > 0 {
		c.note(OutcomeProtected)
	}
	for _, kv := range index {
		if err := c.consume(string(kv.Key), OutcomeIndexRow); err != nil {
			return err
		}
	}
	if counter {
		return c.consume(KeyImportCounter, OutcomeCounter)
	}
	return nil
}

// stage writes one config list through config.Importer; skipped when there is
// nothing to write. It reports refused when core refuses the list's value (a
// *config.FieldError: the importer's documented "the step quarantines" case),
// which the caller turns into quarantine records for what fed the list. Any
// other error (an unregistered path, a closed store) is the step's to fail on:
// it is no fault of the data.
func (g *groupRun) stage(p config.Path, v any, want bool) (refused bool, err error) {
	if !want {
		return false, nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return false, fmt.Errorf("m6: encode %s: %w", p, err)
	}
	if err := g.m.cfg.Config.Stage(g.c.batch(), p, raw, g.m.by); err != nil {
		if _, ok := errors.AsType[*config.FieldError](err); ok {
			g.m.logger.Warn("m6: core refused a config list", "path", string(p), "err", err.Error())
			return true, nil
		}
		return false, fmt.Errorf("m6: stage %s: %w", p, err)
	}
	return false, nil
}

// quarantineRoots moves aside everything that fed a refused roots list: root_dir,
// each import_path row and the path aliases attached to them.
func (g *groupRun) quarantineRoots(planned []plannedRoot) error {
	for _, p := range planned {
		if p.member != "" {
			if err := g.quarantineMember(p.member, p.raw, reasonListRefused); err != nil {
				return err
			}
		} else if err := g.c.quarantine(p.key, p.raw, reasonListRefused); err != nil {
			return err
		}
		for _, a := range p.aliases {
			if err := g.quarantineMember(a.name, a.raw, reasonListRefused); err != nil {
				return err
			}
		}
	}
	return nil
}

func rootSpecs(ps []plannedRoot) []library.RootSpec {
	out := make([]library.RootSpec, len(ps))
	for i, p := range ps {
		out[i] = p.spec
	}
	return out
}

// badRootPath returns the quarantine reason for a path that cannot be a root
// entry, "" when it can: library resolves a root's path once, from an absolute
// host path.
func badRootPath(p string) string {
	if strings.TrimSpace(p) == "" || strings.ContainsRune(p, 0) || !filepath.IsAbs(p) {
		return reasonRootPath
	}
	return ""
}

func nonEmpty(in []string) []string {
	var out []string
	for _, s := range in {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

// mappings converts blob itunes.path_mappings into library.Mapping entries:
// Local is To resolved against the planned roots (the longest one containing it),
// Fold is set for a Windows-form From (HEAD's normalizeWindowsPrefix). A mapping
// whose To is under no root is quarantined raw, and the rest go on. refused is
// the roots core refused (planned is then empty): a mapping that would have
// resolved against one of them is quarantined as such, not as unmappable.
func (g *groupRun) mappings(planned, refused []plannedRoot) ([]mappingEntry, error) {
	var out []mappingEntry
	itunes, present := g.top["itunes"]
	if !present {
		return nil, nil
	}
	var sub map[string]json.RawMessage
	if err := json.Unmarshal(itunes, &sub); err != nil {
		return nil, g.quarantineMember("itunes", itunes, reasonBlobUndecodable)
	}
	raw, present := sub["path_mappings"]
	if !present {
		return nil, nil
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, g.quarantineMember("itunes.path_mappings", raw, reasonBlobUndecodable)
	}
	for i, er := range entries {
		member := fmt.Sprintf("itunes.path_mappings.%d", i)
		var pm headPathMap
		if err := json.Unmarshal(er, &pm); err != nil || strings.TrimSpace(pm.From) == "" {
			if err := g.quarantineMember(member, er, reasonBlobUndecodable); err != nil {
				return nil, err
			}
			continue
		}
		local, ok := resolve(planned, pm.To)
		if !ok {
			why := reasonUnmappable
			if _, under := resolve(refused, pm.To); under {
				why = reasonMappingRootRefused
			}
			if err := g.quarantineMember(member, er, why); err != nil {
				return nil, err
			}
			continue
		}
		out = append(out, mappingEntry{member: member, raw: er, m: library.Mapping{Source: itunesSource, Remote: pm.From, Local: local, Fold: windowsForm(pm.From)}})
	}
	return out, nil
}

// resolve is library's Resolve for a host path: the root with the longest path
// containing p, and p's slash-separated remainder.
func resolve(planned []plannedRoot, p string) (library.Path, bool) {
	if strings.TrimSpace(p) == "" || strings.ContainsRune(p, 0) || !filepath.IsAbs(p) {
		return library.Path{}, false
	}
	best := -1
	for i, r := range planned {
		if library.WithinRoot(p, r.spec.Path) && (best < 0 || len(filepath.Clean(r.spec.Path)) > len(filepath.Clean(planned[best].spec.Path))) {
			best = i
		}
	}
	if best < 0 {
		return library.Path{}, false
	}
	rel, err := filepath.Rel(filepath.Clean(planned[best].spec.Path), filepath.Clean(p))
	if err != nil {
		return library.Path{}, false
	}
	if rel == "." {
		rel = ""
	}
	out := library.Path{Root: planned[best].spec.ID, Rel: filepath.ToSlash(rel)}
	if out.Validate() != nil {
		return library.Path{}, false
	}
	return out, true
}

// windowsForm reports a From written as a Windows path: a drive form, a UNC
// form, or a file URL of either (HEAD's normalizeWindowsPrefix inputs).
func windowsForm(from string) bool {
	s := strings.TrimPrefix(from, "file://localhost/")
	if s == from {
		s = strings.TrimPrefix(from, "file:///")
	}
	if strings.HasPrefix(s, `\\`) {
		return true
	}
	return len(s) >= 2 && s[1] == ':' && (s[0] >= 'a' && s[0] <= 'z' || s[0] >= 'A' && s[0] <= 'Z')
}

// pathAliases attaches blob path_aliases to the planned roots. core's Alias has
// no root of its own: it renders its root's Path (core/library Alias). HEAD's
// PathAlias.Root is matched as a path prefix, most-specific-first
// (web/src/utils/pathAliases.ts matchAlias; internal/config PathAlias), so for
// each root the alias HEAD renders its paths with is the one with the longest
// Root that is the root's path or an ancestor of it:
//
//   - an alias whose Root is the root's path is attached as is (every such
//     alias, in list order, as before);
//   - with none, an alias whose Root is an ancestor is carried by appending the
//     root's path relative to it, in each form HEAD renders (derivedAlias), so
//     the root renders as HEAD rendered it. One alias can feed several roots.
//
// An alias no root takes is quarantined raw, and the reason says its display
// rendering is not carried: one whose Root is inside a root (HEAD seeds those
// from an iTunes mapping whose To is below a root: config.SeedPathAliases) has
// no representation in core's per-root model; one over roots that all have a
// more specific alias rendered nothing at HEAD that a root reaches; one near no
// root has no root to render.
func (g *groupRun) pathAliases(planned []plannedRoot) error {
	raw, present := g.top["path_aliases"]
	if !present {
		return nil
	}
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err != nil {
		return g.quarantineMember("path_aliases", raw, reasonBlobUndecodable)
	}
	type alias struct {
		a      headPathAlias
		root   string // the cleaned Root, "" when it is no absolute path
		member rawMember
	}
	var all []alias
	for i, ar := range list {
		member := fmt.Sprintf("path_aliases.%d", i)
		var a headPathAlias
		if err := json.Unmarshal(ar, &a); err != nil {
			if err := g.quarantineMember(member, ar, reasonBlobUndecodable); err != nil {
				return err
			}
			continue
		}
		root := ""
		if badRootPath(a.Root) == "" {
			root = filepath.Clean(a.Root)
		}
		all = append(all, alias{a: a, root: root, member: rawMember{name: member, raw: ar}})
	}
	taken := make([]bool, len(all))
	for j := range planned {
		rp := filepath.Clean(planned[j].spec.Path)
		var exact []int
		ancestor := ""
		for k, al := range all {
			switch {
			case al.root == "":
			case al.root == rp:
				exact = append(exact, k)
			case library.WithinRoot(rp, al.root) && len(al.root) > len(ancestor):
				ancestor = al.root
			}
		}
		if len(exact) > 0 {
			for _, k := range exact {
				a := all[k].a
				planned[j].spec.Aliases = append(planned[j].spec.Aliases, library.Alias{Windows: a.Windows, UNC: a.UNC, SMBURL: a.SMBURL})
				planned[j].aliases = append(planned[j].aliases, all[k].member)
				taken[k] = true
			}
			continue
		}
		if ancestor == "" {
			continue
		}
		rel, err := filepath.Rel(ancestor, rp)
		if err != nil {
			continue
		}
		for k, al := range all {
			if al.root != ancestor {
				continue
			}
			planned[j].spec.Aliases = append(planned[j].spec.Aliases, derivedAlias(al.a, filepath.ToSlash(rel)))
			planned[j].aliases = append(planned[j].aliases, al.member)
			taken[k] = true
		}
	}
	for k, al := range all {
		if taken[k] {
			continue
		}
		if err := g.quarantineMember(al.member.name, al.member.raw, aliasReason(al.root, planned)); err != nil {
			return err
		}
	}
	return nil
}

// aliasReason is why an alias no root took is quarantined.
func aliasReason(root string, planned []plannedRoot) string {
	if root == "" {
		return reasonAliasNoRoot
	}
	for _, p := range planned {
		if library.WithinRoot(root, p.spec.Path) {
			return reasonAliasSubRoot
		}
	}
	for _, p := range planned {
		if library.WithinRoot(p.spec.Path, root) {
			return reasonAliasShadowed
		}
	}
	return reasonAliasNoRoot
}

// derivedAlias is a, an alias of an ancestor of a root, as an alias of the root
// rel (slash-separated, non-empty) below it: each form is HEAD's rendering of
// the root's own path (web/src/utils/pathAliases.ts toWindows and toSmbURL), so
// a path under the root renders through it exactly as HEAD rendered it through
// a. An empty form stays empty.
func derivedAlias(a headPathAlias, rel string) library.Alias {
	return library.Alias{Windows: windowsJoin(a.Windows, rel), UNC: windowsJoin(a.UNC, rel), SMBURL: smbJoin(a.SMBURL, rel)}
}

// windowsJoin is toWindows: one trailing separator run trimmed from prefix, then
// a backslash and rel with every slash flipped.
func windowsJoin(prefix, rel string) string {
	if prefix == "" {
		return ""
	}
	return strings.TrimRight(prefix, `\/`) + `\` + strings.ReplaceAll(rel, "/", `\`)
}

// smbJoin is toSmbURL: base, a slash, and each segment of rel percent-encoded as
// encodeURIComponent does.
func smbJoin(base, rel string) string {
	if base == "" {
		return ""
	}
	segs := strings.Split(rel, "/")
	for i, s := range segs {
		segs[i] = encodeURIComponent(s)
	}
	return base + "/" + strings.Join(segs, "/")
}

// encodeURIComponent is JavaScript's: every UTF-8 byte except A-Z a-z 0-9 and
// - _ . ! ~ * ' ( ) is written %XX with upper-case hex.
func encodeURIComponent(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', strings.IndexByte("-_.!~*'()", c) >= 0:
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&15])
		}
	}
	return b.String()
}
