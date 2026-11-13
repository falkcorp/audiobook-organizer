// file: core/health/sysinfo/sysinfo.go
// version: 1.0.0
// guid: 747decc9-e246-4d80-92f1-e6a9faf4a284
// last-edited: 2026-11-13

// Package sysinfo reports the host and process facts of GET
// /api/core/v1/system/info and the diagnostics bundle's core.system section
// (05-health §5H.3.6). It is the core re-implementation of ABO's
// internal/sysinfo host half: the memory and uptime platform queries behave as
// they did, and the part that read ABO's run rows stays in books. Nothing here
// counts library contents; per-root capacity comes from library.Root.Usage.
//
// Every platform query degrades to zero: a field the platform cannot report is
// 0, never an error and never a guess.
package sysinfo

import (
	"context"
	"runtime"
	"sync"
	"time"

	"github.com/falkcorp/mediacore/buildid"
	"github.com/falkcorp/mediacore/library"
	"github.com/falkcorp/mediacore/version"
)

// Info is one reading of the host and the process.
type Info struct {
	App           string // set by the caller: Collect does not know the app's ID
	Version       string
	Commit        string
	GoVersion     string
	OS, Arch      string
	NumCPU        int
	GOMAXPROCS    int
	Goroutines    int
	Memory        Memory        // runtime.MemStats subset + process RSS + host total/available
	ProcessUptime time.Duration // since the process started; 0 when the platform cannot say
	SystemUptime  time.Duration // 0 when the platform cannot say
	Roots         []RootSpace   // per library root: name, free/total bytes (via core/library)
	Container     bool          // running in a container (affects updater policy, 05-updater)
}

// Memory is the process's and the host's memory, in bytes.
type Memory struct {
	HeapAlloc, HeapSys, Sys, RSS, HostTotal, HostAvailable uint64
	NumGC                                                  uint32
}

// RootSpace is one library root's capacity. A root that is disabled, unmounted
// or slow to answer reports zeros.
type RootSpace struct {
	Name                  string
	FreeBytes, TotalBytes uint64
}

// rootTimeout bounds one root's statfs: a hung network mount must not hold the
// whole reading.
const rootTimeout = 3 * time.Second

// host is what Collect reads outside the Go runtime. Tests inject a fake; the
// real one is hostReader{}.
type host interface {
	totalMemory() uint64
	availableMemory() uint64
	rss() uint64
	systemUptime() time.Duration
	container() bool
	// processStart is when the process started, or the zero Time when the
	// platform cannot say (ProcessUptime is then 0).
	processStart() time.Time
}

// usageRoot is the part of *library.Root that Collect reads, so the capacity
// pool is testable without a library manager.
type usageRoot interface {
	Spec() library.RootSpec
	Usage(ctx context.Context) (library.Usage, error)
}

// Collect reads the host and the process now. roots are the library roots whose
// capacity to report; a nil or empty slice reports none. ctx bounds the
// per-root reads.
func Collect(ctx context.Context, roots []*library.Root) Info {
	rs := make([]usageRoot, len(roots))
	for i, r := range roots {
		rs[i] = r
	}
	return collect(ctx, rs, hostReader{}, time.Now)
}

func collect(ctx context.Context, roots []usageRoot, h host, now func() time.Time) Info {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	var up time.Duration
	if st := h.processStart(); !st.IsZero() {
		up = max(0, now().Sub(st))
	}
	info := Info{
		ProcessUptime: up,
		Version:       version.Version,
		Commit:        buildid.Commit(),
		GoVersion:     runtime.Version(),
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
		NumCPU:        runtime.NumCPU(),
		GOMAXPROCS:    runtime.GOMAXPROCS(0),
		Goroutines:    runtime.NumGoroutine(),
		SystemUptime:  h.systemUptime(),
		Container:     h.container(),
		Memory: Memory{
			HeapAlloc:     ms.HeapAlloc,
			HeapSys:       ms.HeapSys,
			Sys:           ms.Sys,
			RSS:           h.rss(),
			HostTotal:     h.totalMemory(),
			HostAvailable: h.availableMemory(),
			NumGC:         ms.NumGC,
		},
		Roots: rootSpaces(ctx, roots),
	}
	return info
}

// rootSpaces reads every root's capacity concurrently (a bounded pool: one
// slot per CPU, at most the number of roots), each through library.Call under
// rootTimeout so a hung mount is abandoned rather than awaited, and returns
// them in the order given. Each worker writes only its own element of the
// result: the sets are disjoint.
func rootSpaces(ctx context.Context, roots []usageRoot) []RootSpace {
	out := make([]RootSpace, len(roots))
	sem := make(chan struct{}, max(1, min(runtime.NumCPU(), len(roots))))
	var wg sync.WaitGroup
	for i, r := range roots {
		spec := r.Spec()
		out[i].Name = spec.Name
		if out[i].Name == "" {
			out[i].Name = string(spec.ID)
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			u, err := library.Call(ctx, rootTimeout, func() (library.Usage, error) { return r.Usage(ctx) })
			if err == nil {
				out[i].TotalBytes, out[i].FreeBytes = u.Total, u.Avail
			}
		}()
	}
	wg.Wait()
	return out
}
