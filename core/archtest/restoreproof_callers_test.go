// file: core/archtest/restoreproof_callers_test.go
// version: 0.1.0
// guid: 87c16caf-c161-4862-b8d7-df048a64c497
// last-edited: 2026-11-20

package archtest

import (
	"maps"
	"strings"
	"testing"
)

// restoreProofTree is a fixture core module: backup declaring WithRestoreProof
// and reading it back, backuphttp minting the proof, a plain package, and a
// test file that mints one. extra adds or replaces files.
func restoreProofTree(t *testing.T, extra map[string]string) string {
	t.Helper()
	files := map[string]string{
		"go.mod": "module github.com/falkcorp/mediacore\n\ngo 1.27.0\n",
		"backup/authorize.go": `package backup

type RestoreProof struct{ Owner bool }

// WithRestoreProof is named in this comment, which is not a use.
func WithRestoreProof(ctx any, p RestoreProof) any { return p }

func RestoreProofFrom(ctx any) RestoreProof { p, _ := ctx.(RestoreProof); return p }
`,
		"internal/backuphttp/routes.go": `package backuphttp

import "github.com/falkcorp/mediacore/backup"

func restoreCtx(ctx any) any { return backup.WithRestoreProof(ctx, backup.RestoreProof{Owner: true}) }
`,
		"gotham/gotham.go": `package gotham

func Signal() string { return "bat" }
`,
		"gotham/gotham_test.go": `package gotham

import (
	"testing"

	"github.com/falkcorp/mediacore/backup"
)

func TestSignal(t *testing.T) { _ = backup.WithRestoreProof(nil, backup.RestoreProof{Owner: true}) }
`,
	}
	maps.Copy(files, extra)
	return writeModule(t, files)
}

// TestArchtest_RestoreProofCallers (F8-04 A1 review m3): backuphttp, backup's
// declaration and test files are not reported; any other use, through any
// import form or from backup's own files, is; the guard refuses a tree without
// backup; and real core passes.
func TestArchtest_RestoreProofCallers(t *testing.T) {
	one := func(t *testing.T, file, body string) {
		t.Helper()
		got, err := RestoreProofCallerFindings(restoreProofTree(t, map[string]string{file: body}))
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || !strings.Contains(got[0], file) || !strings.Contains(got[0], "backup.WithRestoreProof") {
			t.Errorf("findings = %q, want exactly one naming %s", got, file)
		}
	}
	t.Run("AllowedNotReported", func(t *testing.T) {
		got, err := RestoreProofCallerFindings(restoreProofTree(t, nil))
		if err != nil || len(got) != 0 {
			t.Errorf("findings = %q, %v; want none", got, err)
		}
	})
	t.Run("Call", func(t *testing.T) {
		one(t, "gotham/mint.go", "package gotham\n\nimport \"github.com/falkcorp/mediacore/backup\"\n\nfunc Mint() any { return backup.WithRestoreProof(nil, backup.RestoreProof{Owner: true}) }\n")
	})
	t.Run("AliasedValue", func(t *testing.T) {
		one(t, "gotham/mint.go", "package gotham\n\nimport bk \"github.com/falkcorp/mediacore/backup\"\n\nvar mint = bk.WithRestoreProof\n")
	})
	t.Run("DotImport", func(t *testing.T) {
		one(t, "gotham/mint.go", "package gotham\n\nimport . \"github.com/falkcorp/mediacore/backup\"\n\nfunc Mint() any { return WithRestoreProof(nil, RestoreProof{Owner: true}) }\n")
	})
	t.Run("BackupTaskBody", func(t *testing.T) {
		one(t, "backup/tasks.go", "package backup\n\nfunc stageBody(ctx any) any { return WithRestoreProof(ctx, RestoreProof{Owner: true}) }\n")
	})
	t.Run("Linkname", func(t *testing.T) {
		one(t, "gotham/mint.go", "package gotham\n\nimport _ \"unsafe\"\n\n//go:linkname mint github.com/falkcorp/mediacore/backup.WithRestoreProof\nfunc mint(ctx any, p any) any\n")
	})
	t.Run("SiblingOfBackuphttpNotExempt", func(t *testing.T) {
		one(t, "internal/backuphttpx/mint.go", "package backuphttpx\n\nimport \"github.com/falkcorp/mediacore/backup\"\n\nvar mint = backup.WithRestoreProof\n")
	})
	t.Run("VacuousTreeRefused", func(t *testing.T) {
		root := writeModule(t, map[string]string{
			"go.mod":           "module github.com/falkcorp/mediacore\n\ngo 1.27.0\n",
			"gotham/gotham.go": "package gotham\n",
		})
		if _, err := RestoreProofCallerFindings(root); err == nil {
			t.Error("a tree without backup passed")
		}
	})
	t.Run("Core", func(t *testing.T) {
		RestoreProofCallers(t)
	})
}
