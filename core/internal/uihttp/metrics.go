// file: core/internal/uihttp/metrics.go
// version: 1.0.0
// guid: 5b0d3e6a-2c71-4f98-b4d5-8e1a07c93f42
// last-edited: 2026-11-10

package uihttp

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/falkcorp/mediacore/metrics"
)

// otherLabel is the one bucket every value outside a closed set is counted in.
const otherLabel = "other"

// Both routes take input from a browser, and csp-report takes it from any
// caller at all, so no submitted string ever becomes a label: each label
// below is chosen from a closed set written here (or, for a module, from the
// IDs the kernel registered), and everything else is "other". The series count
// of each family is therefore fixed at registration, whatever is sent.

// diagnosticKinds are the kinds the shell reports (07 §7.5.2's two tables and
// 07 §7.14's app-id-mismatch), in the order the golden test expects them.
func diagnosticKinds() []string {
	return []string{
		"core-version-incompatible", "ui-missing", "version-incompatible", "undeclared-descriptor",
		"module-not-enabled", "descriptor-without-ui", "app-id-mismatch",
	}
}

// cspDirectives are the CSP directive names a violation report can name (CSP
// Level 3's fetch, document, navigation and trusted-types directives).
func cspDirectives() []string {
	return []string{
		"default-src", "script-src", "script-src-elem", "script-src-attr", "style-src", "style-src-elem",
		"style-src-attr", "img-src", "font-src", "connect-src", "media-src", "object-src", "frame-src",
		"child-src", "worker-src", "manifest-src", "prefetch-src", "base-uri", "form-action",
		"frame-ancestors", "navigate-to", "sandbox", "require-trusted-types-for", "trusted-types",
		"plugin-types",
	}
}

// kindLabel is raw's bucket: the closed kind it equals, else "other".
func kindLabel(raw string) string {
	if slices.Contains(diagnosticKinds(), raw) {
		return raw
	}
	return otherLabel
}

// directiveLabel is the closed directive a report's directive text names, else
// "other". A legacy report's violated-directive carries the source list after
// the name ("script-src 'self'"), so only the first word counts, in lower case.
func directiveLabel(raw string) string {
	f := strings.Fields(raw)
	if len(f) == 0 {
		return otherLabel
	}
	name := strings.ToLower(f[0])
	if slices.Contains(cspDirectives(), name) {
		return name
	}
	return otherLabel
}

// moduleLabel is id when it is one of the registered module IDs, else "other".
func moduleLabel(registered []string, id string) string {
	if id != "" && slices.Contains(registered, id) {
		return id
	}
	return otherLabel
}

// uiMetrics holds the two instruments of the core "ui" scope this package
// records (07 §7.16). The scope is created once by the kernel and shared with
// modulehttp and ui.Handler (metrics.ErrScopeTaken). A nil *uiMetrics records
// nothing.
type uiMetrics struct {
	diagnostics metric.Int64Counter
	csp         metric.Int64Counter
}

// newMetrics creates the counters on sc and exports every label combination
// of the closed sets from zero. A nil scope is a nil result.
func newMetrics(sc *metrics.Scope, modules []string) (*uiMetrics, error) {
	if sc == nil {
		return nil, nil
	}
	var m uiMetrics
	var err error
	if m.diagnostics, err = sc.Meter().Int64Counter(sc.Prefix()+".diagnostics",
		metric.WithDescription("UI diagnostics the shell reported, by kind and module (07 §7.16).")); err != nil {
		return nil, fmt.Errorf("uihttp: diagnostics counter: %w", err)
	}
	if m.csp, err = sc.Meter().Int64Counter(sc.Prefix()+".csp_reports",
		metric.WithDescription("Content-Security-Policy violation reports, by directive (07 §7.16).")); err != nil {
		return nil, fmt.Errorf("uihttp: csp reports counter: %w", err)
	}
	ctx := context.Background()
	for _, k := range append(diagnosticKinds(), otherLabel) {
		for _, mod := range append(slices.Clone(modules), otherLabel) {
			m.diagnostics.Add(ctx, 0, diagnosticAttrs(k, mod))
		}
	}
	for _, d := range append(cspDirectives(), otherLabel) {
		m.csp.Add(ctx, 0, cspAttrs(d))
	}
	return &m, nil
}

func diagnosticAttrs(kind, module string) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String("kind", kind), attribute.String("module", module))
}

func cspAttrs(directive string) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String("directive", directive))
}

// recordDiagnostic counts one diagnostic; kind and module are already labels.
func (m *uiMetrics) recordDiagnostic(ctx context.Context, kind, module string) {
	if m != nil {
		m.diagnostics.Add(ctx, 1, diagnosticAttrs(kind, module))
	}
}

// recordCSP counts one violation report; directive is already a label.
func (m *uiMetrics) recordCSP(ctx context.Context, directive string) {
	if m != nil {
		m.csp.Add(ctx, 1, cspAttrs(directive))
	}
}
