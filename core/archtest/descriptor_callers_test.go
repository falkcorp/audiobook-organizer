// file: core/archtest/descriptor_callers_test.go
// version: 1.0.0
// guid: c00ccfb4-20f5-412b-b0f0-1cb83cf41645
// last-edited: 2026-11-11

package archtest

import (
	"maps"
	"strings"
	"testing"
)

// descriptorTree is a fixture core module: integrations declaring
// NewDescriptor, a kind package (notify) wrapping it in its typed
// constructor, an adapter built through that constructor, and a
// test-support package and a test file that call NewDescriptor directly.
// extra adds or replaces files.
func descriptorTree(t *testing.T, extra map[string]string) string {
	t.Helper()
	files := map[string]string{
		"go.mod": "module github.com/falkcorp/mediacore\n\ngo 1.27.0\n",
		"integrations/integrations.go": `package integrations

type Instance interface{ Close() error }

type Descriptor struct{ name string }

func NewDescriptor[T Instance](name string, build func() (T, error)) Descriptor { return Descriptor{name: name} }
`,
		"integrations/notify/notify.go": `package notify

import "github.com/falkcorp/mediacore/integrations"

type Notifier interface{ integrations.Instance }

func Adapter(name string, build func() (Notifier, error)) integrations.Descriptor {
	return integrations.NewDescriptor(name, build)
}
`,
		"integrations/notify/batsignal/batsignal.go": `package batsignal

import (
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/notify"
)

func Descriptor() integrations.Descriptor { return notify.Adapter("core.batsignal", nil) }
`,
		"integrations/integrationstest/fakes.go": `package integrationstest

import "github.com/falkcorp/mediacore/integrations"

func Fake() integrations.Descriptor { return integrations.NewDescriptor[integrations.Instance]("hello.fake", nil) }
`,
		"integrations/notify/batsignal/batsignal_test.go": `package batsignal

import (
	"testing"

	"github.com/falkcorp/mediacore/integrations"
)

func TestDirect(t *testing.T) { _ = integrations.NewDescriptor[integrations.Instance]("hello.x", nil) }
`,
	}
	maps.Copy(files, extra)
	return writeModule(t, files)
}

// TestArchtest_IntegrationsDescriptorCallers (05-metadata M1.9): a kind
// package's call is not reported; an adapter package's direct call is,
// once; every other shape of reference is counted; the guard refuses a
// tree without integrations; and real core passes.
func TestArchtest_IntegrationsDescriptorCallers(t *testing.T) {
	t.Run("KindPackageNotReported", func(t *testing.T) {
		got, err := IntegrationsDescriptorCallerFindings(descriptorTree(t, nil))
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Fatalf("the allowed tree reported:\n  %s", strings.Join(got, "\n  "))
		}
	})

	t.Run("AdapterPackageReportedOnce", func(t *testing.T) {
		root := descriptorTree(t, map[string]string{
			"integrations/notify/batsignal/batsignal.go": `package batsignal

import "github.com/falkcorp/mediacore/integrations"

type signal struct{}

func (signal) Close() error { return nil }

func Descriptor() integrations.Descriptor {
	return integrations.NewDescriptor("core.batsignal", func() (signal, error) { return signal{}, nil })
}
`,
		})
		got, err := IntegrationsDescriptorCallerFindings(root)
		if err != nil {
			t.Fatal(err)
		}
		if n := findingsByFile(root, got)["integrations/notify/batsignal/batsignal.go"]; n != 1 || len(got) != 1 {
			t.Fatalf("want one report in batsignal.go, got:\n  %s", strings.Join(got, "\n  "))
		}
	})

	t.Run("EveryShapeCounted", func(t *testing.T) {
		root := descriptorTree(t, map[string]string{
			// an alias and a function value
			"tasks/tasks.go": `package tasks

import gordon "github.com/falkcorp/mediacore/integrations"

var build = gordon.NewDescriptor[gordon.Instance]

func Register() { _ = build; _ = gordon.NewDescriptor[gordon.Instance]("hello.y", nil) }
`,
			// a dot import
			"outbox/outbox.go": `package outbox

import . "github.com/falkcorp/mediacore/integrations"

func Drain() Descriptor { return NewDescriptor[Instance]("hello.z", nil) }
`,
			// a blank import references nothing
			"events/events.go": "package events\n\nimport _ \"github.com/falkcorp/mediacore/integrations\"\n",
			// a module package, seen only under the integration tag set
			"library/library.go": "package library\n",
			"library/library_integration.go": `//go:build integration

package library

import "github.com/falkcorp/mediacore/integrations"

func Scan() { _ = integrations.NewDescriptor[integrations.Instance]("hello.w", nil) }
`,
			// a linkname reaches it without an import
			"health/health.go": `package health

import _ "unsafe"

//go:linkname direct github.com/falkcorp/mediacore/integrations.NewDescriptor
func direct()
`,
			// a sibling of the kind packages is not a kind package
			"integrations/gadgets/gadgets.go": `package gadgets

import "github.com/falkcorp/mediacore/integrations"

var _ = integrations.NewDescriptor[integrations.Instance]
`,
		})
		got, err := IntegrationsDescriptorCallerFindings(root)
		if err != nil {
			t.Fatal(err)
		}
		byFile := findingsByFile(root, got)
		for file, want := range map[string]int{
			"tasks/tasks.go":                  2,
			"outbox/outbox.go":                1,
			"library/library_integration.go":  1,
			"health/health.go":                1,
			"integrations/gadgets/gadgets.go": 1,
		} {
			if byFile[file] != want {
				t.Errorf("%s: %d reports, want %d", file, byFile[file], want)
			}
		}
		if byFile["events/events.go"] != 0 {
			t.Error("a blank import was reported")
		}
		if len(got) != 6 {
			t.Errorf("%d reports, want 6:\n  %s", len(got), strings.Join(got, "\n  "))
		}
	})

	t.Run("VacuousTreeRefused", func(t *testing.T) {
		root := writeModule(t, map[string]string{
			"go.mod":         "module github.com/falkcorp/mediacore\n\ngo 1.27.0\n",
			"tasks/tasks.go": "package tasks\n",
		})
		if _, err := IntegrationsDescriptorCallerFindings(root); err == nil {
			t.Error("a tree without integrations passed")
		}
	})

	t.Run("Core", func(t *testing.T) {
		IntegrationsDescriptorCallers(t)
	})
}
