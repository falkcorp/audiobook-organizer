// file: core/kernel/metrics_test.go
// version: 1.0.0
// guid: 3a6cee84-a972-4f47-b094-89037d629194
// last-edited: 2026-11-07

package kernel

import (
	"bufio"
	"net/http"
	"os"
	"slices"
	"strings"
)

// scrapeFamilies reads a /metrics response in the Prometheus text format and
// returns the sorted names of the families (their "# TYPE" lines) that start
// with prefix. A family with no sample has no TYPE line, so this is what a
// scraper sees. (prometheus/common/expfmt is not on core's third-party
// allow-list, and the TYPE line is all this needs.)
func scrapeFamilies(resp *http.Response, prefix string) ([]string, error) {
	var out []string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 4 && f[0] == "#" && f[1] == "TYPE" && strings.HasPrefix(f[2], prefix) {
			out = append(out, f[2])
		}
	}
	slices.Sort(out)
	return out, sc.Err()
}

// readFamilies reads a family-list golden: one family name per line; blank
// lines and # comments are ignored.
func readFamilies(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line != "" && !strings.HasPrefix(line, "#") {
			out = append(out, line)
		}
	}
	return out, sc.Err()
}

// bootPhases returns the phase labels of the boot-phase totals (step "") k's
// Provider serves, sorted.
func bootPhases(k *Kernel) ([]string, error) {
	mfs, err := k.provider.Gatherer().Gather()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, mf := range mfs {
		if !strings.HasSuffix(mf.GetName(), "_kernel_boot_phase_seconds") {
			continue
		}
		for _, m := range mf.GetMetric() {
			var phase, step string
			for _, lp := range m.GetLabel() {
				switch lp.GetName() {
				case "phase":
					phase = lp.GetValue()
				case "step":
					step = lp.GetValue()
				}
			}
			if step == "" {
				out = append(out, phase)
			}
		}
	}
	slices.Sort(out)
	return out, nil
}
