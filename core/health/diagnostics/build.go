// file: core/health/diagnostics/build.go
// version: 1.0.1
// guid: 0e48d09a-7583-4133-9749-d96bbfaf2aa5
// last-edited: 2026-11-13

package diagnostics

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"slices"
	"time"

	"github.com/falkcorp/mediacore/log"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

// Exporter is the native body of core.diagnostics.export (05-health 5H.3.5):
// it writes the sections the start-time viewer may read into
// "<run-id>.zip" inside an os.Root on core.diagnostics.dir, one zip entry open
// at a time. Sections run sequentially because the zip writer is not
// concurrent; a section that fans out over a library does so inside its own
// Write with tasks.RunItems.

// Source is a section's data: a value the bundle serialises as JSON. A source
// that holds secret-typed values hands over Secret and Redacted placeholders
// (the typed layer); everything else is still pattern-redacted.
type Source func(ctx context.Context) (any, error)

// TextSource is a section's text (log lines, a metrics exposition). The reader
// is closed by the bundle.
type TextSource func(ctx context.Context) (io.ReadCloser, error)

// CoreSources are the core sections' producers, injected by the kernel so this
// package imports none of the services behind them. A nil source drops its
// section. core.manifest and core.build need none.
type CoreSources struct {
	System  Source     // core.system: sysinfo.Info
	Config  Source     // core.config: effective config with source layers; secrets as Secret
	Health  Source     // core.health: health.Snapshot with Detail
	Tasks   Source     // core.tasks: recent runs; params and results only for kinds the viewer may view
	Storage Source     // core.storage: keyspace sizes, engine metrics
	Outbox  Source     // core.outbox: consumer lag, dead-letter headers (payloads omitted)
	Logs    TextSource // core.logs: JSONL records from the in-memory ring
	Metrics TextSource // core.metrics: Prometheus text exposition
}

// MetricsSink counts bundles and redactions. health.Metrics implements it; a
// nil sink counts nothing.
type MetricsSink interface {
	Bundle(outcome string)
	Redacted(layer string, n int)
}

// DefaultMaxFileBytes is core.diagnostics.max_file_bytes' default: 64 MiB.
const DefaultMaxFileBytes = 64 << 20

// ManifestSection is one included section in the manifest. Perm is what the
// download re-checks.
type ManifestSection struct {
	Name string   `json:"name"`
	Perm PermSpec `json:"perm"`
}

// SectionError is a section that failed. The bundle continues without it
// ("partial bundle beats none").
type SectionError struct {
	Section string `json:"section"`
	Error   string `json:"error"`
}

// Manifest is core.manifest/manifest.json.
type Manifest struct {
	BundleVersion int               `json:"bundle_version"`
	App           string            `json:"app"`
	Version       string            `json:"version"`
	CreatedAt     time.Time         `json:"created_at"`
	ActorID       string            `json:"actor_id"`
	Sections      []ManifestSection `json:"sections"`
	Omitted       []string          `json:"omitted,omitempty"`
	Errors        []SectionError    `json:"errors,omitempty"`
	Truncated     []string          `json:"truncated,omitempty"`
	Redactions    map[string]int    `json:"redactions"`
}

// ManifestSectionName and ManifestFile locate the manifest in the zip.
const (
	ManifestSectionName = "core.manifest"
	ManifestFile        = ManifestSectionName + "/manifest.json"
)

// Config builds an Exporter.
type Config struct {
	// Root is the os.Root on core.diagnostics.dir. Every path the bundle
	// touches goes through it, so none can escape.
	Root *os.Root
	// ViewerFor resolves the permissions of the run's actor when the run
	// starts. The run outlives the request, so the viewer is looked up, not
	// carried.
	ViewerFor func(ctx context.Context, actorID string) (Viewer, error)
	Sources   CoreSources
	// Contributors are the modules' sections (Registrar.Diagnostics).
	Contributors []Contributor
	// Redactor is the pattern layer; nil means log.NewRedactor(nil).
	Redactor *log.Redactor
	Metrics  MetricsSink
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// App and Version go in the manifest.
	App, Version string
	// MaxFileBytes caps one file; zero means DefaultMaxFileBytes.
	MaxFileBytes int64
	// BuildInfo is debug.ReadBuildInfo unless a test replaces it.
	BuildInfo func() (*debug.BuildInfo, bool)
}

// Exporter writes bundles. It holds no per-run state, so one serves every run.
type Exporter struct {
	cfg      Config
	sections []Section
}

// manage is the permission every core section needs: what starting a bundle
// needs.
func manage() rbac.Requirement { return rbac.Need("core:system.manage") }

// New validates cfg and assembles the section list: the core sections, then the
// contributors'. A malformed section (bad name, no Write, zero or delegated
// Perm, a duplicate name, the reserved core.manifest) is an error here, at
// boot, not a hole in a bundle later.
func New(cfg Config) (*Exporter, error) {
	if cfg.Root == nil {
		return nil, errors.New("diagnostics: Config.Root is nil")
	}
	if cfg.ViewerFor == nil {
		return nil, errors.New("diagnostics: Config.ViewerFor is nil")
	}
	if cfg.Redactor == nil {
		cfg.Redactor = log.NewRedactor(nil)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.MaxFileBytes <= 0 {
		cfg.MaxFileBytes = DefaultMaxFileBytes
	}
	if cfg.BuildInfo == nil {
		cfg.BuildInfo = debug.ReadBuildInfo
	}
	e := &Exporter{cfg: cfg}
	all := e.coreSections()
	for _, c := range cfg.Contributors {
		if c == nil {
			return nil, errors.New("diagnostics: nil Contributor")
		}
		all = append(all, c.Sections()...)
	}
	seen := map[string]bool{ManifestSectionName: true}
	for _, s := range all {
		switch {
		case !validSectionName(s.Name):
			return nil, fmt.Errorf("diagnostics: section name %q is not <moduleID>.<name>", s.Name)
		case seen[s.Name]:
			return nil, fmt.Errorf("diagnostics: section %q is registered twice (or is reserved)", s.Name)
		case s.Write == nil:
			return nil, fmt.Errorf("diagnostics: section %q has no Write", s.Name)
		case !usable(specOf(s.Perm)):
			return nil, fmt.Errorf("diagnostics: section %q has no usable Perm (zero, delegated or legacy)", s.Name)
		}
		seen[s.Name] = true
	}
	e.sections = all
	return e, nil
}

// SectionNames lists the sections an unrestricted run would write, in order.
func (e *Exporter) SectionNames() []string {
	out := make([]string, len(e.sections))
	for i, s := range e.sections {
		out[i] = s.Name
	}
	return out
}

// usable reports whether a Perm can ever be satisfied: a zero, delegated or
// legacy requirement, or an empty scope list, is a section no one could read.
func usable(p PermSpec) bool {
	switch p.Kind {
	case "public", "authenticated":
		return true
	case "all_of", "any_of":
		return len(p.Perms) > 0
	}
	return false
}

func jsonSection(name, file string, src Source) Section {
	return Section{Name: name, Perm: manage(), Write: func(ctx context.Context, w Writer, _ Viewer) error {
		v, err := src(ctx)
		if err != nil {
			return err
		}
		return w.JSON(file, v)
	}}
}

func textSection(name, file string, src TextSource) Section {
	return Section{Name: name, Perm: manage(), Write: func(ctx context.Context, w Writer, _ Viewer) error {
		rc, err := src(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = rc.Close() }()
		return w.File(file, rc)
	}}
}

// coreSections is 5H.3.5's table minus core.manifest, which the build writes
// last because it records what the others did.
func (e *Exporter) coreSections() []Section {
	src := e.cfg.Sources
	var out []Section
	add := func(name, file string, s Source) {
		if s != nil {
			out = append(out, jsonSection(name, file, s))
		}
	}
	add("core.system", "info.json", src.System)
	out = append(out, jsonSection("core.build", "build.json", func(context.Context) (any, error) {
		bi, ok := e.cfg.BuildInfo()
		if !ok || bi == nil {
			return map[string]string{"error": "build info unavailable"}, nil
		}
		return bi, nil
	}))
	add("core.config", "config.json", src.Config)
	add("core.health", "health.json", src.Health)
	add("core.tasks", "tasks.json", src.Tasks)
	if src.Logs != nil {
		out = append(out, textSection("core.logs", "logs.jsonl", src.Logs))
	}
	if src.Metrics != nil {
		out = append(out, textSection("core.metrics", "metrics.txt", src.Metrics))
	}
	add("core.storage", "storage.json", src.Storage)
	add("core.outbox", "outbox.json", src.Outbox)
	return out
}

// build is one run's state.
type build struct {
	zw        *zip.Writer
	red       *log.Redactor
	counts    counts
	written   map[string]bool
	truncated []string
	limit     int64
	now       func() time.Time
}

// ValidRunID reports whether id is safe as a file name stem: letters, digits,
// underscore and hyphen only. A run ID that is not never reaches the
// filesystem.
func ValidRunID(id string) bool { return plainName(id) }

// plainName is ^[A-Za-z0-9_-]{1,128}$: a name safe as one path element.
func plainName(s string) bool {
	if len(s) == 0 || len(s) > 128 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// BundleFile is the zip's name for a run.
func BundleFile(runID string) string { return runID + ".zip" }

// Export is the kind's Run: it matches InterimExport.Export, so F8's native
// body keeps the types, and reports progress per section.
func (e *Exporter) Export(rc *tasks.RC, p ExportParams) (ExportResult, error) {
	return e.Build(rc.Context(), string(rc.RunID()), p, func(done, total int) {
		rc.Progress().Set(int64(done), tasks.Known(int64(total)))
	})
}

// Build writes the bundle for runID. progress (nil allowed) is called after
// each section with how many are done of how many are planned.
func (e *Exporter) Build(ctx context.Context, runID string, p ExportParams, progress func(done, total int)) (res ExportResult, err error) {
	if !ValidRunID(runID) {
		return ExportResult{}, fmt.Errorf("diagnostics: run id %q is not a plain name", runID)
	}
	plan, err := e.plan(p.Sections)
	if err != nil {
		return ExportResult{}, err
	}
	viewer, err := e.cfg.ViewerFor(ctx, p.ActorID)
	if err != nil {
		e.countBundle("error")
		return ExportResult{}, fmt.Errorf("diagnostics: resolve viewer: %w", err)
	}
	viewer.ActorID = p.ActorID

	tmp, final := runID+".zip.tmp", BundleFile(runID)
	f, err := e.cfg.Root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		e.countBundle("error")
		return ExportResult{}, fmt.Errorf("diagnostics: create %s: %w", tmp, err)
	}
	finished := false
	defer func() {
		if !finished {
			// The run already failed with err; a file that will not close or
			// will not go is reported beside it, never instead of it.
			err = errors.Join(err, f.Close(), e.cfg.Root.Remove(tmp))
			e.countBundle("error")
		}
	}()

	st := &build{
		zw:      zip.NewWriter(f),
		red:     e.cfg.Redactor,
		written: map[string]bool{},
		limit:   e.cfg.MaxFileBytes,
		now:     e.cfg.Now,
	}
	m := Manifest{
		BundleVersion: BundleVersion,
		App:           e.cfg.App,
		Version:       e.cfg.Version,
		CreatedAt:     e.cfg.Now().UTC(),
		ActorID:       p.ActorID,
		Sections:      []ManifestSection{},
	}
	for i, sec := range plan {
		if err := ctx.Err(); err != nil {
			return ExportResult{}, err
		}
		if !viewer.Satisfies(sec.Perm) {
			m.Omitted = append(m.Omitted, sec.Name)
			st.counts.permission++
		} else {
			m.Sections = append(m.Sections, ManifestSection{Name: sec.Name, Perm: specOf(sec.Perm)})
			if serr := e.runSection(ctx, st, sec, viewer); serr != nil {
				if cerr := ctx.Err(); cerr != nil {
					return ExportResult{}, cerr
				}
				m.Errors = append(m.Errors, SectionError{Section: sec.Name, Error: st.redactString("error", serr.Error())})
			}
		}
		if progress != nil {
			progress(i+1, len(plan))
		}
	}
	m.Truncated = slices.Clone(st.truncated)
	m.Redactions = st.counts.byLayer()
	if err := st.writeManifest(m); err != nil {
		return ExportResult{}, err
	}
	if err := st.zw.Close(); err != nil {
		return ExportResult{}, fmt.Errorf("diagnostics: finish zip: %w", err)
	}
	if err := f.Sync(); err != nil {
		return ExportResult{}, fmt.Errorf("diagnostics: sync zip: %w", err)
	}
	if err := f.Close(); err != nil {
		return ExportResult{}, fmt.Errorf("diagnostics: close zip: %w", err)
	}
	if err := e.cfg.Root.Rename(tmp, final); err != nil {
		return ExportResult{}, fmt.Errorf("diagnostics: publish zip: %w", err)
	}
	finished = true

	outcome := "ok"
	if len(m.Errors) > 0 || len(m.Truncated) > 0 {
		outcome = "partial"
	}
	e.countBundle(outcome)
	for layer, n := range m.Redactions {
		e.countRedacted(layer, n)
	}
	return ExportResult{File: final, Redactions: m.Redactions}, nil
}

// plan picks the sections to run: every one, or the requested names. An
// unknown name is an error so a typo is not a silently empty bundle.
func (e *Exporter) plan(want []string) ([]Section, error) {
	if len(want) == 0 {
		return e.sections, nil
	}
	var out []Section
	for _, s := range e.sections {
		if slices.Contains(want, s.Name) {
			out = append(out, s)
		}
	}
	for _, n := range want {
		if !slices.ContainsFunc(e.sections, func(s Section) bool { return s.Name == n }) {
			return nil, fmt.Errorf("diagnostics: unknown section %q", n)
		}
	}
	return out, nil
}

// runSection runs one section, turning a panic into an error so one bad
// producer cannot take the bundle down.
func (e *Exporter) runSection(ctx context.Context, st *build, sec Section, v Viewer) (err error) {
	w := &sectionWriter{st: st, section: sec.Name}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("section panicked: %v", r)
		}
	}()
	return sec.Write(ctx, w, v)
}

// writeManifest writes core.manifest/manifest.json. It does not pass through
// the section writer (the manifest is the account of what that did) but its
// free text, the section errors, was redacted when recorded.
func (st *build) writeManifest(m Manifest) error {
	zw, err := st.zw.CreateHeader(&zip.FileHeader{Name: ManifestFile, Method: zip.Deflate, Modified: st.now()})
	if err != nil {
		return fmt.Errorf("diagnostics: create manifest: %w", err)
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("diagnostics: marshal manifest: %w", err)
	}
	if _, err := zw.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("diagnostics: write manifest: %w", err)
	}
	return nil
}

func (e *Exporter) countBundle(outcome string) {
	if e.cfg.Metrics != nil {
		e.cfg.Metrics.Bundle(outcome)
	}
}

func (e *Exporter) countRedacted(layer string, n int) {
	if e.cfg.Metrics != nil {
		e.cfg.Metrics.Redacted(layer, n)
	}
}
