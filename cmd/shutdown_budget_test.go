// file: cmd/shutdown_budget_test.go
// version: 1.0.0
// guid: 9d74254a-3c28-49d1-aa57-070a758cc2fb
// last-edited: 2026-11-07

package cmd

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestShutdownBudgetFromEnv(t *testing.T) {
	tests := []struct {
		name    string
		set     bool
		value   string
		want    time.Duration
		wantErr bool
	}{
		{name: "unset", set: false, want: 0},
		{name: "empty", set: true, value: "", want: 0},
		{name: "seconds", set: true, value: "25s", want: 25 * time.Second},
		{name: "minute", set: true, value: "1m", want: time.Minute},
		{name: "garbage", set: true, value: "abc", wantErr: true},
		{name: "negative", set: true, value: "-5s", wantErr: true},
		{name: "zero", set: true, value: "0s", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lookup := func(key string) (string, bool) {
				if key != shutdownBudgetEnv {
					t.Fatalf("looked up %q, want %q", key, shutdownBudgetEnv)
				}
				return tc.value, tc.set
			}
			got, err := shutdownBudgetFromEnv(lookup)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("got %v, nil error; want an error", got)
				}
				if !strings.Contains(err.Error(), "ABO_CORE_SHUTDOWN_BUDGET") {
					t.Fatalf("error %q does not name the variable", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

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
