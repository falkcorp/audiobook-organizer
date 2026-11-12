// file: core/archtest/outbox_consumers_test.go
// version: 1.0.0
// guid: 54934e35-d277-4710-ac50-66d316042e52
// last-edited: 2026-11-12

package archtest

import (
	"maps"
	"strings"
	"testing"
)

// outboxTree is a fixture core module: outbox declaring the consumer
// constructors, a core package consuming through the plain form, and a module
// package (examples/gotham) consuming through ConsumeIntoTask. extra adds or
// replaces files.
func outboxTree(t *testing.T, extra map[string]string) string {
	t.Helper()
	files := map[string]string{
		"go.mod": "module github.com/falkcorp/mediacore\n\ngo 1.27.0\n",
		"outbox/outbox.go": `package outbox

type Handler struct{}

func Consume(consumer string) Handler        { return Handler{} }
func ConsumeTx(consumer string) Handler      { return Handler{} }
func ConsumeRaw(consumer string) Handler     { return Handler{} }
func ConsumeIntoTask(consumer string) Handler { return Handler{} }
`,
		"notify/notify.go": `package notify

import "github.com/falkcorp/mediacore/outbox"

func Handlers() []outbox.Handler { return []outbox.Handler{outbox.Consume("core.notify.deliver")} }
`,
		"examples/gotham/gotham.go": `package gotham

import "github.com/falkcorp/mediacore/outbox"

func Handlers() []outbox.Handler { return []outbox.Handler{outbox.ConsumeIntoTask("gotham.signal.respond")} }
`,
		"examples/gotham/gotham_test.go": `package gotham

import (
	"testing"

	"github.com/falkcorp/mediacore/outbox"
)

func TestPlain(t *testing.T) { _ = outbox.Consume("gotham.test") }
`,
	}
	maps.Copy(files, extra)
	return writeModule(t, files)
}

// TestArchtest_ModuleConsumeFlagged (05-events §5E.12, 06 §6.4.10): a core
// package's plain consumer and a module's ConsumeIntoTask are not reported; a
// module package's Consume, ConsumeTx and ConsumeRaw are, in every shape of
// reference; a test file is exempt; the guard refuses a tree it cannot look
// at; and real core passes.
func TestArchtest_ModuleConsumeFlagged(t *testing.T) {
	t.Run("AllowedShapesNotReported", func(t *testing.T) {
		got, err := OutboxModuleConsumerFindings(outboxTree(t, nil))
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Fatalf("the allowed tree reported:\n  %s", strings.Join(got, "\n  "))
		}
	})

	t.Run("ModuleConsumeReportedOnce", func(t *testing.T) {
		root := outboxTree(t, map[string]string{
			"examples/gotham/gotham.go": `package gotham

import "github.com/falkcorp/mediacore/outbox"

func Handlers() []outbox.Handler { return []outbox.Handler{outbox.Consume("gotham.signal.respond")} }
`,
		})
		got, err := OutboxModuleConsumerFindings(root)
		if err != nil {
			t.Fatal(err)
		}
		if n := findingsByFile(root, got)["examples/gotham/gotham.go"]; n != 1 || len(got) != 1 {
			t.Fatalf("want one report in gotham.go, got:\n  %s", strings.Join(got, "\n  "))
		}
	})

	t.Run("EveryShapeCounted", func(t *testing.T) {
		root := outboxTree(t, map[string]string{
			// an alias, a function value and the other two plain forms
			"examples/gotham/alias.go": `package gotham

import ob "github.com/falkcorp/mediacore/outbox"

var build = ob.ConsumeTx

func Register() { _ = build; _ = ob.ConsumeRaw("gotham.x") }
`,
			// a dot import
			"examples/gotham/dot.go": `package gotham

import . "github.com/falkcorp/mediacore/outbox"

func Drain() Handler { return Consume("gotham.y") }
`,
			// a blank import references nothing
			"examples/wayne/wayne.go": "package wayne\n\nimport _ \"github.com/falkcorp/mediacore/outbox\"\n",
			// a sub-package of a module package is a module package
			"examples/gotham/arkham/arkham.go": `package arkham

import "github.com/falkcorp/mediacore/outbox"

var _ = outbox.Consume
`,
			// a linkname reaches it without an import
			"examples/gotham/link.go": `package gotham

import _ "unsafe"

//go:linkname direct github.com/falkcorp/mediacore/outbox.Consume
func direct()
`,
			// seen only under the integration tag set
			"examples/gotham/integration.go": `//go:build integration

package gotham

import "github.com/falkcorp/mediacore/outbox"

func Scan() { _ = outbox.Consume("gotham.w") }
`,
			// a core package may consume however it likes
			"tasks/tasks.go": `package tasks

import "github.com/falkcorp/mediacore/outbox"

func Handlers() { _ = outbox.ConsumeRaw("core.tasks.x"); _ = outbox.ConsumeTx("core.tasks.y") }
`,
		})
		got, err := OutboxModuleConsumerFindings(root)
		if err != nil {
			t.Fatal(err)
		}
		byFile := findingsByFile(root, got)
		for file, want := range map[string]int{
			"examples/gotham/alias.go":         2,
			"examples/gotham/dot.go":           1,
			"examples/gotham/arkham/arkham.go": 1,
			"examples/gotham/link.go":          1,
			"examples/gotham/integration.go":   1,
		} {
			if byFile[file] != want {
				t.Errorf("%s: %d reports, want %d", file, byFile[file], want)
			}
		}
		if byFile["examples/wayne/wayne.go"] != 0 || byFile["tasks/tasks.go"] != 0 || byFile["examples/gotham/gotham_test.go"] != 0 {
			t.Errorf("a blank import, a core package or a test file was reported: %v", byFile)
		}
		if len(got) != 6 {
			t.Errorf("%d reports, want 6:\n  %s", len(got), strings.Join(got, "\n  "))
		}
	})

	t.Run("VacuousTreesRefused", func(t *testing.T) {
		noOutbox := writeModule(t, map[string]string{
			"go.mod":                    "module github.com/falkcorp/mediacore\n\ngo 1.27.0\n",
			"examples/gotham/gotham.go": "package gotham\n",
		})
		if _, err := OutboxModuleConsumerFindings(noOutbox); err == nil {
			t.Error("a tree without outbox passed")
		}
		noModules := writeModule(t, map[string]string{
			"go.mod":           "module github.com/falkcorp/mediacore\n\ngo 1.27.0\n",
			"outbox/outbox.go": "package outbox\n",
			"tasks/tasks.go":   "package tasks\n",
		})
		if _, err := OutboxModuleConsumerFindings(noModules); err == nil {
			t.Error("a tree with no module package passed")
		}
	})

	t.Run("Core", func(t *testing.T) {
		OutboxModuleConsumersAreTasks(t)
	})
}
