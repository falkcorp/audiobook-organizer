// file: core/examples/hello/count.go
// version: 1.0.3
// guid: 1b89cd69-630b-4f09-9c3d-c5b86b40ba47
// last-edited: 2026-11-11

package hello

import (
	"fmt"
	"strconv"

	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

type CountParams struct{}

type CountResult struct {
	Names     int64 `json:"names"`
	Greetings int64 `json:"greetings"`
}

func countTask(s *service) tasks.Definition {
	return tasks.Task("hello.count", tasks.TaskSpec[CountParams, CountResult]{
		Title:   "Count recorded names",
		Help:    "Sums the per-name counters. Read-only.",
		Effects: tasks.ReadOnly(tasks.Res(string(ID), "name")),
		Perms:   tasks.Permissions{Start: rbac.Need(PermAdmin)},
		Resume:  tasks.ResumeFromZero,
		Run: func(rc *tasks.RC, _ CountParams) (CountResult, error) {
			var kvs []storage.KV
			for kv, err := range s.names.Scan(rc.Context(), storage.ScanOptions{}) {
				if err != nil {
					return CountResult{}, fmt.Errorf("hello: scan: %w", err)
				}
				kvs = append(kvs, kv.Clone())
			}
			res, err := tasks.RunItems(rc, tasks.CPU(), kvs,
				func(ic *tasks.ItemRC, kv storage.KV) error {
					n, err := strconv.ParseInt(string(kv.Value), 10, 64)
					if err != nil {
						return fmt.Errorf("hello: bad count at %q: %w", kv.Key, err)
					}
					ic.Count("names", 1) // atomic, folded into res.Counters
					ic.Count("greetings", n)
					return nil
				},
				tasks.ItemsOptions[storage.KV]{
					Label: func(kv storage.KV) string { return string(kv.Key) }, // pure: item only
				})
			return CountResult{Names: res.Counters["names"], Greetings: res.Counters["greetings"]}, err
		},
	})
}
