// file: cmd/shutdown_budget_test.go
// version: 1.0.0
// guid: 9d74254a-3c28-49d1-aa57-070a758cc2fb
// last-edited: 2026-11-07

package cmd

import (
	"os"
	"strings"
	"testing"
)

func TestDeployFilesCarryStopTimeouts(t *testing.T) {
	compose, err := os.ReadFile("../docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"stop_grace_period: 60s", "ABO_CORE_SHUTDOWN_BUDGET=25s"} {
		if !strings.Contains(string(compose), want) {
			t.Errorf("docker-compose.yml lacks %q", want)
		}
	}
	plist, err := os.ReadFile("../deploy/com.audiobook-organizer.plist")
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(plist))
	joined := strings.Join(fields, " ")
	if !strings.Contains(joined, "<key>ExitTimeOut</key> <integer>60</integer>") {
		t.Errorf("plist lacks <key>ExitTimeOut</key> followed by <integer>60</integer>")
	}
}
