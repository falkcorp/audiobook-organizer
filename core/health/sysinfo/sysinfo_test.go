// file: core/health/sysinfo/sysinfo_test.go
// version: 1.0.1
// guid: e5f2b8a4-3c71-4d96-b0e8-9a4d6c1f7b35
// last-edited: 2026-11-13

package sysinfo

import (
	"context"
	"errors"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/library"
)

type fakeHost struct {
	total, avail, rssBytes uint64
	up                     time.Duration
	start                  time.Time
	inContainer            bool
}

func (f fakeHost) totalMemory() uint64     { return f.total }
func (f fakeHost) availableMemory() uint64 { return f.avail }
func (f fakeHost) rss() uint64             { return f.rssBytes }
func (f fakeHost) systemUptime() time.Duration {
	return f.up
}
func (f fakeHost) processStart() time.Time { return f.start }
func (f fakeHost) container() bool         { return f.inContainer }

type fakeRoot struct {
	spec  library.RootSpec
	usage library.Usage
	err   error
	block chan struct{} // non-nil: Usage blocks on it, ignoring ctx (a hung mount)
}

func (r fakeRoot) Spec() library.RootSpec { return r.spec }
func (r fakeRoot) Usage(context.Context) (library.Usage, error) {
	if r.block != nil {
		<-r.block
	}
	return r.usage, r.err
}

func TestCollect_FillsRuntimeAndHostFields(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	h := fakeHost{total: 64 << 30, avail: 32 << 30, rssBytes: 200 << 20, up: 36 * time.Hour, start: now.Add(-90 * time.Second), inContainer: true}
	info := collect(t.Context(), nil, h, func() time.Time { return now })
	if info.OS != runtime.GOOS || info.Arch != runtime.GOARCH || info.GoVersion != runtime.Version() {
		t.Errorf("platform fields = %s/%s %s", info.OS, info.Arch, info.GoVersion)
	}
	if info.NumCPU != runtime.NumCPU() || info.GOMAXPROCS != runtime.GOMAXPROCS(0) || info.Goroutines < 1 {
		t.Errorf("cpu fields = %d %d %d", info.NumCPU, info.GOMAXPROCS, info.Goroutines)
	}
	if info.Version == "" {
		t.Error("Version is empty")
	}
	m := info.Memory
	if m.HostTotal != 64<<30 || m.HostAvailable != 32<<30 || m.RSS != 200<<20 {
		t.Errorf("host memory = %+v", m)
	}
	if m.HeapAlloc == 0 || m.HeapSys == 0 || m.Sys == 0 {
		t.Errorf("runtime memory = %+v", m)
	}
	if info.ProcessUptime != 90*time.Second || info.SystemUptime != 36*time.Hour || !info.Container {
		t.Errorf("uptime/container = %s %s %v", info.ProcessUptime, info.SystemUptime, info.Container)
	}
	if info.App != "" {
		t.Errorf("App = %q: the caller sets it", info.App)
	}
	if len(info.Roots) != 0 {
		t.Errorf("Roots = %v, want none", info.Roots)
	}
}

// TestCollect_UnknownIsZero: what the platform cannot say is 0, never an error.
func TestCollect_UnknownIsZero(t *testing.T) {
	info := collect(t.Context(), nil, fakeHost{}, time.Now)
	if info.SystemUptime != 0 || info.ProcessUptime != 0 || info.Memory.HostTotal != 0 || info.Memory.RSS != 0 || info.Container {
		t.Errorf("an empty host reading = %+v", info)
	}
}

func TestCollect_RealHostDoesNotPanic(t *testing.T) {
	info := Collect(t.Context(), nil)
	if info.Version == "" || info.Goroutines < 1 {
		t.Fatalf("Collect = %+v", info)
	}
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		if info.Memory.HostTotal == 0 {
			t.Errorf("no host memory total on %s", runtime.GOOS)
		}
		if info.SystemUptime <= 0 {
			t.Errorf("no system uptime on %s", runtime.GOOS)
		}
		if info.ProcessUptime <= 0 || info.ProcessUptime > 24*time.Hour {
			t.Errorf("process uptime on %s = %s, want a test-run-sized value", runtime.GOOS, info.ProcessUptime)
		}
		if info.Memory.RSS == 0 {
			t.Errorf("no RSS on %s", runtime.GOOS)
		}
	}
}

// TestCollect_RootsInOrderWithZerosForTheUnreadable: capacity per root, in the
// order given; a disabled root (error) and a hung one report zeros and do not
// hold up the others beyond the bound.
func TestCollect_RootsInOrderWithZerosForTheUnreadable(t *testing.T) {
	hung := make(chan struct{})
	defer close(hung)
	roots := []usageRoot{
		fakeRoot{spec: library.RootSpec{ID: "r1", Name: "arkham"}, usage: library.Usage{Total: 100, Avail: 40}},
		fakeRoot{spec: library.RootSpec{ID: "r2"}, usage: library.Usage{Total: 7, Avail: 3}}, // no name: the ID
		fakeRoot{spec: library.RootSpec{ID: "r3", Name: "blackgate"}, err: errors.New("unmounted")},
		fakeRoot{spec: library.RootSpec{ID: "r4", Name: "narrows"}, block: hung},
	}
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond) // the caller's bound beats rootTimeout
	defer cancel()
	got := rootSpaces(ctx, roots)
	want := []RootSpace{{"arkham", 40, 100}, {"r2", 3, 7}, {"blackgate", 0, 0}, {"narrows", 0, 0}}
	if !slices.Equal(got, want) {
		t.Errorf("roots = %+v, want %+v", got, want)
	}
	if len(rootSpaces(t.Context(), nil)) != 0 {
		t.Error("no roots must give no entries")
	}
}

func TestParseMeminfo(t *testing.T) {
	const text = "MemTotal:       16384000 kB\nMemFree:         1000 kB\nMemAvailable:    8192000 kB\nBogus: x kB\nNoColon\n"
	total, avail := parseMeminfo(text)
	if total != 16384000*1024 || avail != 8192000*1024 {
		t.Errorf("parseMeminfo = %d %d", total, avail)
	}
	if total, avail := parseMeminfo(""); total != 0 || avail != 0 {
		t.Errorf("empty meminfo = %d %d", total, avail)
	}
	if total, _ := parseMeminfo("MemTotal: lots kB"); total != 0 {
		t.Errorf("malformed MemTotal = %d, want 0", total)
	}
}

func TestParseStatmRSS(t *testing.T) {
	if got := parseStatmRSS("5000 1234 300 10 0 900 0\n", 4096); got != 1234*4096 {
		t.Errorf("rss = %d", got)
	}
	for _, bad := range []string{"", "5000", "5000 x"} {
		if got := parseStatmRSS(bad, 4096); got != 0 {
			t.Errorf("parseStatmRSS(%q) = %d, want 0", bad, got)
		}
	}
	if parseStatmRSS("1 2", 0) != 0 {
		t.Error("a zero page size must give 0")
	}
}

// TestParseProcessStart: starttime is field 22 of /proc/self/stat, counted from
// the last ")" because comm may contain spaces and parentheses.
func TestParseProcessStart(t *testing.T) {
	// pid (comm) state ppid pgrp session tty tpgid flags minflt cminflt majflt
	// cmajflt utime stime cutime cstime priority nice threads itrealvalue starttime ...
	stat := "4242 (bat (man) x) S 1 4242 4242 0 -1 4194560 100 0 0 0 5 3 0 0 20 0 8 0 150000 99999 100"
	got := parseProcessStart(stat, "cpu  1 2 3\nbtime 1700000000\nprocesses 99\n")
	want := time.Unix(1700000000, 0).Add(1500 * time.Second)
	if !got.Equal(want) {
		t.Errorf("start = %v, want %v", got, want)
	}
	// A sub-second remainder survives: 150001 ticks is 1500.01 s.
	frac := strings.Replace(stat, " 150000 ", " 150001 ", 1)
	if got, want := parseProcessStart(frac, "btime 1700000000\n"), time.Unix(1700000000, 10_000_000).Add(1500*time.Second); !got.Equal(want) {
		t.Errorf("start with a remainder = %v, want %v", got, want)
	}
	// A corrupt /proc cannot overflow the arithmetic: a huge starttime or boot
	// time is malformed, not a wrapped time.
	huge := strings.Replace(stat, " 150000 ", " 18446744073709551615 ", 1)
	for _, c := range []struct{ stat, proc string }{
		{huge, "btime 1700000000\n"}, {stat, "btime 9223372036854775807\n"}, {stat, "btime -5\n"},
		{"", "btime 1"}, {"1 (x) S 1 2", "btime 1"}, {stat, ""}, {stat, "btime nope\n"},
	} {
		if !parseProcessStart(c.stat, c.proc).IsZero() {
			t.Errorf("parseProcessStart(%q, %q) was not zero", c.stat, c.proc)
		}
	}
}

func TestParseUptime(t *testing.T) {
	if got := parseUptime("12345.67 9999.1\n"); got != time.Duration(12345.67*float64(time.Second)) {
		t.Errorf("uptime = %s", got)
	}
	for _, bad := range []string{"", "soon", "-5 1"} {
		if got := parseUptime(bad); got != 0 {
			t.Errorf("parseUptime(%q) = %s, want 0", bad, got)
		}
	}
}

func TestDetectContainer(t *testing.T) {
	none := func(string) bool { return false }
	only := func(p string) func(string) bool { return func(q string) bool { return q == p } }
	cases := []struct {
		name   string
		env    string
		exists func(string) bool
		cgroup string
		want   bool
	}{
		{"bare host", "", none, "0::/init.scope\n", false},
		{"docker marker file", "", only("/.dockerenv"), "", true},
		{"podman marker file", "", only("/run/.containerenv"), "", true},
		{"container env", "podman", none, "", true},
		{"docker cgroup", "", none, "12:cpu:/docker/abc123\n", true},
		{"kubernetes cgroup", "", none, "0::/kubepods/burstable/pod1\n", true},
		{"a user slice is not a container", "", none, "0::/user.slice/user-1000.slice/session-2.scope\n", false},
	}
	for _, c := range cases {
		if got := detectContainer(c.env, c.exists, c.cgroup); got != c.want {
			t.Errorf("%s: detectContainer = %v, want %v", c.name, got, c.want)
		}
	}
}
