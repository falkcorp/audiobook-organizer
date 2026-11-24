// file: core/kernel/identity_tokenfile_test.go
// version: 0.1.0
// guid: b3e81f46-9d27-4c5a-8e10-6a4d2c97f3b5
// last-edited: 2026-11-21

package kernel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/auth"
)

// Batman names only.

// TestIdentityBoot_BootstrapTokenFileName: the name an app sets in
// Config.AuthOptions.BootstrapTokenFile is the file P4d's boot writes carry the
// token in, in the state directory, and nothing is written under core's
// default; unset keeps core's name. A name that cannot be a file there stops
// the boot instead of booting without a token file.
func TestIdentityBoot_BootstrapTokenFileName(t *testing.T) {
	boot := func(t *testing.T, name string) (string, error) {
		t.Helper()
		h, c, _ := mountConfig(t, true)
		c.AuthOptions.BootstrapTokenFile = name
		_, err := h.boot(c) // h.boot closes the kernel when the test ends
		return h.state, err
	}
	token := func(t *testing.T, dir, name string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, name))
		if os.IsNotExist(err) {
			return ""
		}
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(b))
	}

	t.Run("configured name", func(t *testing.T) {
		dir, err := boot(t, ".bootstrap-token")
		if err != nil {
			t.Fatal(err)
		}
		if token(t, dir, ".bootstrap-token") == "" {
			t.Error("no token under the configured name")
		}
		if token(t, dir, auth.BootstrapTokenFile) != "" {
			t.Errorf("a token was also written under core's default %q", auth.BootstrapTokenFile)
		}
	})
	t.Run("unset keeps core's name", func(t *testing.T) {
		dir, err := boot(t, "")
		if err != nil {
			t.Fatal(err)
		}
		if token(t, dir, auth.BootstrapTokenFile) == "" {
			t.Errorf("no token under core's default %q", auth.BootstrapTokenFile)
		}
	})
	t.Run("a bad name stops the boot", func(t *testing.T) {
		dir, err := boot(t, "../escape")
		if err == nil {
			t.Fatal("a token file name outside the state directory booted")
		}
		if token(t, dir, auth.BootstrapTokenFile) != "" {
			t.Error("a refused boot still wrote a token")
		}
	})
}
