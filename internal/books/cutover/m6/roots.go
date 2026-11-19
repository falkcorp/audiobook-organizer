// file: internal/books/cutover/m6/roots.go
// version: 1.0.0
// guid: 5c6b9d2a-5df4-4e1e-9d64-7a0f3a1c1b52
// last-edited: 2026-11-19

package m6

import (
	"context"
	"encoding/json"
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
			}})
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
		})
	}

	// path_aliases: an alias belongs to the root whose path is its Root; one that
	// matches none is kept raw rather than attached to a root it was not for.
	if raw, present := g.top["path_aliases"]; present {
		var aliases []json.RawMessage
		if err := json.Unmarshal(raw, &aliases); err != nil {
			if err := g.quarantineMember("path_aliases", raw, reasonBlobUndecodable); err != nil {
				return err
			}
		}
		for i, ar := range aliases {
			var a headPathAlias
			member := fmt.Sprintf("path_aliases.%d", i)
			if err := json.Unmarshal(ar, &a); err != nil {
				if err := g.quarantineMember(member, ar, reasonBlobUndecodable); err != nil {
					return err
				}
				continue
			}
			target := -1
			for j := range planned {
				if a.Root != "" && filepath.Clean(planned[j].spec.Path) == filepath.Clean(a.Root) {
					target = j
					break
				}
			}
			if target < 0 {
				if err := g.quarantineMember(member, ar, reasonAliasNoRoot); err != nil {
					return err
				}
				continue
			}
			planned[target].spec.Aliases = append(planned[target].spec.Aliases, library.Alias{Windows: a.Windows, UNC: a.UNC, SMBURL: a.SMBURL})
		}
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
	mappings, err := g.mappings(planned)
	if err != nil {
		return err
	}

	// protected_paths.
	var protected []string
	if raw, present := g.top["protected_paths"]; present {
		if err := json.Unmarshal(raw, &protected); err != nil {
			if err := g.quarantineMember("protected_paths", raw, reasonBlobUndecodable); err != nil {
				return err
			}
			protected = nil
		}
	}
	protected = nonEmpty(protected)

	// Stage. A refusal of the whole list is an error, not a quarantine: the lists
	// are one config value each, so a refusal names no one row, and failing the
	// step leaves every source row where it is.
	if err := g.stage(pathRoots, rootSpecs(planned), len(planned) > 0); err != nil {
		return err
	}
	if err := g.stage(pathMappings, mappings, len(mappings) > 0); err != nil {
		return err
	}
	if err := g.stage(pathProtected, protected, len(protected) > 0); err != nil {
		return err
	}
	for _, p := range planned {
		m.report(Entry{Kind: "root", ID: string(p.spec.ID), Detail: p.spec.Role.String()})
		if p.scan == nil {
			continue
		}
		if !m.cfg.DryRun {
			val, err := json.Marshal(p.scan)
			if err != nil {
				return fmt.Errorf("m6: encode the scan record of %s: %w", p.spec.ID, err)
			}
			if err := c.w.Set([]byte(PrefixRootScan+string(p.spec.ID)), val); err != nil {
				return err
			}
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
// nothing to write or in a dry run.
func (g *groupRun) stage(p config.Path, v any, want bool) error {
	if !want || g.m.cfg.DryRun {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("m6: encode %s: %w", p, err)
	}
	if err := g.m.cfg.Config.Stage(g.c.batch(), p, raw, g.m.by); err != nil {
		return fmt.Errorf("m6: stage %s: %w", p, err)
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
// whose To is under no root is quarantined raw, and the rest go on.
func (g *groupRun) mappings(planned []plannedRoot) ([]library.Mapping, error) {
	var out []library.Mapping
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
			if err := g.quarantineMember(member, er, reasonUnmappable); err != nil {
				return nil, err
			}
			continue
		}
		out = append(out, library.Mapping{Source: itunesSource, Remote: pm.From, Local: local, Fold: windowsForm(pm.From)})
		g.m.report(Entry{Kind: "mapping", ID: member, Detail: string(local.Root)})
		g.c.note(OutcomeMapping)
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
