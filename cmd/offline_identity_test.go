// file: cmd/offline_identity_test.go
// version: 0.1.0
// guid: 2d8f4a60-b7c1-4e93-8a52-0c6e1f9d3b74
// last-edited: 2026-11-21

package cmd

import (
	"bytes"
	"strings"
	"testing"
)

// TestCLIExposesTheIdentityOfflineCommands (F4-08 B9b; 05-authn section 4.14,
// F4-08 Q6a): the three operator commands of the identity flip are commands of
// ABO's CLI at their paths, with core's help text, and need no per-command
// wrapper: core's generic offline wrapper carries them. Help is answered
// before any config resolves, so it runs without a store. Running them against
// ABO's store is B10's: ABO has not declared the identity boot, and until it
// does they refuse (kernel's and core/app's tests pin that).
func TestCLIExposesTheIdentityOfflineCommands(t *testing.T) {
	for _, tc := range []struct {
		path []string
		help string
	}{
		{[]string{"users", "reset-password"}, "Usage: users reset-password <username>"},
		{[]string{"storage", "journal-queue"}, "Usage: storage journal-queue list | clear <seq> --confirm"},
		{[]string{"storage", "cutover-blockers"}, ""},
	} {
		name := strings.Join(tc.path, " ")
		got, rest, err := rootCmd.Find(tc.path)
		if err != nil || len(rest) != 0 || got.CommandPath() != "audiobook-organizer "+name {
			t.Fatalf("%s: the CLI has %q (rest %v, %v)", name, got.CommandPath(), rest, err)
		}
		if got.Long == "" || !strings.Contains(got.Long, "service stopped") && !strings.Contains(got.Long, "service must not be running") && !strings.Contains(got.Long, "service must be stopped") {
			t.Errorf("%s: Long does not say the service must be stopped: %q", name, got.Long)
		}
		if tc.help == "" {
			continue
		}
		var out bytes.Buffer
		rootCmd.SetOut(&out)
		rootCmd.SetErr(&out)
		rootCmd.SetArgs(append(append([]string{}, tc.path...), "--help"))
		t.Cleanup(func() { rootCmd.SetArgs(nil); rootCmd.SetOut(nil); rootCmd.SetErr(nil) })
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("%s --help = %v\n%s", name, err, out.String())
		}
		if !strings.Contains(out.String(), tc.help) {
			t.Errorf("%s --help lacks %q:\n%s", name, tc.help, out.String())
		}
	}
}
