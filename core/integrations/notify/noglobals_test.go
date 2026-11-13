// file: core/integrations/notify/noglobals_test.go
// version: 1.0.0
// guid: c5e18a47-3f92-4d60-b7a1-0e9d82f46c13
// last-edited: 2026-11-13

package notify_test

import (
	"os"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/archtest"
)

// TestNoGlobals_Notify (06 section 6.10): no init and no package-level mutable
// state in the notify packages (the AST scan of archtest.NoInit), and two
// Services share nothing: a rule one holds is not read by the other, and a
// notification published on one never reaches the other's channels.
func TestNoGlobals_Notify(t *testing.T) {
	wd, err := os.Getwd()
	must(t, err)
	root, err := archtest.ModuleRoot(wd)
	must(t, err)
	archtest.NoInit(t, root, "./integrations/notify/...")

	one := newRig(t, rigOpts{})
	two := newRig(t, rigOpts{})
	one.putRule(rule("roof", []string{signalName}, commissioner))
	for _, err := range two.svc.Rules(admin()) {
		if err != nil {
			t.Fatalf("listing the second service's rules: %v", err)
		}
		t.Fatal("a rule put on one Service is listed by the other")
	}
	one.publish(signalNote("Joker"))
	two.publish(signalNote("Penguin"))
	eventually(t, "the first service delivers", func() bool { return len(callsTo(one.fake, commissioner)) == 1 })
	stays(t, "the second service stays quiet", 300*time.Millisecond, func() bool { return len(callsTo(two.fake, commissioner)) == 0 })
}
