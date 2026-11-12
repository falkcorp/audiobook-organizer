// file: core/archtest/bootseam_callers_test.go
// version: 1.0.0
// guid: e8fb5f3a-0e5d-4ada-aae6-00db7f45914e
// last-edited: 2026-11-12

package archtest

import (
	"maps"
	"strings"
	"testing"
)

// bootseamTree is a fixture core module: the bootseam package, the app
// composition root reading Seams.Built, a coretest package and its crashmatrix
// sub-package calling With, a plain package, and a test file that uses both.
// extra adds or replaces files.
func bootseamTree(t *testing.T, extra map[string]string) string {
	t.Helper()
	files := map[string]string{
		"go.mod": "module github.com/falkcorp/mediacore\n\ngo 1.27.0\n",
		"internal/bootseam/bootseam.go": `package bootseam

type Seams struct{ Built func(kernel any) }

func With(parent any, s Seams) any { return s }
`,
		"app/app.go": `package app

import "github.com/falkcorp/mediacore/internal/bootseam"

func New(sm bootseam.Seams) {
	if sm.Built != nil {
		sm.Built(nil)
	}
}
`,
		"coretest/harness.go": `package coretest

import "github.com/falkcorp/mediacore/internal/bootseam"

func Boot() any { return bootseam.With(nil, bootseam.Seams{Built: func(any) {}}) }
`,
		"coretest/crashmatrix/injector.go": `package crashmatrix

import "github.com/falkcorp/mediacore/internal/bootseam"

func Inject() any { return bootseam.With(nil, bootseam.Seams{}) }
`,
		"gotham/gotham.go": `package gotham

func Signal() string { return "bat" }
`,
		"gotham/gotham_test.go": `package gotham

import (
	"testing"

	"github.com/falkcorp/mediacore/internal/bootseam"
)

func TestSeam(t *testing.T) { _ = bootseam.With(nil, bootseam.Seams{Built: func(any) {}}) }
`,
	}
	maps.Copy(files, extra)
	return writeModule(t, files)
}

// TestArchtest_BootseamHookCallers (F2-16 follow-up): the composition root,
// coretest and its sub-packages, and test files are not reported; any other
// non-test file that calls With or touches Built, through any import form, is;
// the guard refuses a tree without bootseam; and real core passes.
func TestArchtest_BootseamHookCallers(t *testing.T) {
	one := func(t *testing.T, extra map[string]string, wantFile, wantWhat string) {
		t.Helper()
		got, err := BootseamHookCallerFindings(bootseamTree(t, extra))
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || !strings.Contains(got[0], wantFile) || !strings.Contains(got[0], wantWhat) {
			t.Errorf("findings = %q, want exactly one naming %s and %s", got, wantFile, wantWhat)
		}
	}

	t.Run("ExemptPackagesAndTestsNotReported", func(t *testing.T) {
		got, err := BootseamHookCallerFindings(bootseamTree(t, nil))
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Errorf("findings = %q, want none", got)
		}
	})

	t.Run("WithCallReported", func(t *testing.T) {
		one(t, map[string]string{"gotham/seam.go": `package gotham

import "github.com/falkcorp/mediacore/internal/bootseam"

func Wire() any { return bootseam.With(nil, bootseam.Seams{}) }
`}, "gotham/seam.go", "bootseam.With")
	})

	t.Run("AliasedWithCallReported", func(t *testing.T) {
		one(t, map[string]string{"gotham/seam.go": `package gotham

import bs "github.com/falkcorp/mediacore/internal/bootseam"

func Wire() any { return bs.With(nil, bs.Seams{}) }
`}, "gotham/seam.go", "bootseam.With")
	})

	t.Run("DotImportedWithReported", func(t *testing.T) {
		one(t, map[string]string{"gotham/seam.go": `package gotham

import . "github.com/falkcorp/mediacore/internal/bootseam"

func Wire() any { return With(nil, Seams{}) }
`}, "gotham/seam.go", "bootseam.With")
	})

	t.Run("WithAsValueReported", func(t *testing.T) {
		one(t, map[string]string{"gotham/seam.go": `package gotham

import "github.com/falkcorp/mediacore/internal/bootseam"

var hook = bootseam.With
`}, "gotham/seam.go", "bootseam.With")
	})

	t.Run("BuiltKeyReported", func(t *testing.T) {
		one(t, map[string]string{"gotham/seam.go": `package gotham

import "github.com/falkcorp/mediacore/internal/bootseam"

var s = bootseam.Seams{Built: func(any) {}}
`}, "gotham/seam.go", "Seams.Built")
	})

	t.Run("BuiltSelectorReported", func(t *testing.T) {
		one(t, map[string]string{"gotham/seam.go": `package gotham

import "github.com/falkcorp/mediacore/internal/bootseam"

func Read(sm bootseam.Seams) bool { return sm.Built != nil }
`}, "gotham/seam.go", "Seams.Built")
	})

	t.Run("LinknameReported", func(t *testing.T) {
		one(t, map[string]string{"gotham/seam.go": `package gotham

import _ "unsafe"

//go:linkname with github.com/falkcorp/mediacore/internal/bootseam.With
func with(parent any, s any) any
`}, "gotham/seam.go", "bootseam.With")
	})

	t.Run("UnrelatedBuiltNotReported", func(t *testing.T) {
		got, err := BootseamHookCallerFindings(bootseamTree(t, map[string]string{"gotham/other.go": `package gotham

type Plan struct{ Built bool }

func Done(p Plan) bool { return p.Built }
`}))
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Errorf("a file that does not import bootseam was reported: %q", got)
		}
	})

	t.Run("SiblingOfAppNotExempt", func(t *testing.T) {
		one(t, map[string]string{"appsupport/seam.go": `package appsupport

import "github.com/falkcorp/mediacore/internal/bootseam"

func Wire() any { return bootseam.With(nil, bootseam.Seams{}) }
`}, "appsupport/seam.go", "bootseam.With")
	})

	t.Run("VacuousTreeRefused", func(t *testing.T) {
		root := writeModule(t, map[string]string{
			"go.mod":         "module github.com/falkcorp/mediacore\n\ngo 1.27.0\n",
			"tasks/tasks.go": "package tasks\n",
		})
		if _, err := BootseamHookCallerFindings(root); err == nil {
			t.Error("a tree without bootseam passed")
		}
	})

	t.Run("Core", func(t *testing.T) {
		BootseamHookCallers(t)
	})
}
