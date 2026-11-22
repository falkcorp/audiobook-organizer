// file: internal/config/project_race_test.go
// version: 1.0.0
// guid: 5b8e2d71-4c06-4f93-a1d7-0e3c9a6b8f24
// last-edited: 2026-11-21

package config

import (
	"fmt"
	"sync"
	"testing"
)

// TestProject_ConcurrentWithSnapshotReaders: Project replaces the whole global
// Config (*c = cfg) on every settings save, while background goroutines read it
// through Snapshot. The two only synchronise through mu, so under `-race` this
// fails the moment mutateLocal stops taking mu.Lock (the writer's struct store
// is then unordered against Snapshot's copy), and it fails on a torn read even
// without -race: every projected Config carries the same tag in three
// fields, so a reader that sees the tags disagree has seen half of one save
// and half of another.
func TestProject_ConcurrentWithSnapshotReaders(t *testing.T) {
	prev := Snapshot()
	t.Cleanup(func() { Project(prev) })

	const (
		saves   = 500
		readers = 16
	)
	tagged := func(i int) Config {
		cfg := prev
		tag := fmt.Sprintf("arkham-%d", i)
		cfg.RootDir = "/gotham/" + tag
		cfg.FolderNamingPattern = tag
		cfg.OwnerEmail = tag + "@wayne.example"
		cfg.Maintenance.Enabled = i%2 == 0
		cfg.Maintenance.WindowStart = i % 24
		return cfg
	}
	Project(tagged(0))

	stop := make(chan struct{})
	var wg sync.WaitGroup
	torn := make(chan string, readers)
	for range readers {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				cfg := Snapshot()
				want := cfg.FolderNamingPattern
				if cfg.RootDir != "/gotham/"+want || cfg.OwnerEmail != want+"@wayne.example" {
					select {
					case torn <- fmt.Sprintf("torn read: root=%q pattern=%q owner=%q", cfg.RootDir, want, cfg.OwnerEmail):
					default:
					}
					return
				}
				_ = cfg.Maintenance.Enabled
				_ = cfg.Maintenance.WindowStart
			}
		})
	}

	for i := 1; i <= saves; i++ {
		Project(tagged(i))
	}
	close(stop)
	wg.Wait()
	select {
	case msg := <-torn:
		t.Fatal(msg)
	default:
	}
}
