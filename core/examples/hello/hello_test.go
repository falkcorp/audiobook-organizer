// file: core/examples/hello/hello_test.go
// version: 1.0.1
// guid: 9824ef03-77bf-470b-bc4d-e48e03ef10d5
// last-edited: 2026-11-07

package hello_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/coretest/logcapture"
	"github.com/falkcorp/mediacore/examples/hello"
)

// TestHelloEndToEnd is 06 §6.10's row for the R1 surface: boot, GET
// /api/hello/v1/greeting over a real listener, a clean stop. The task, the
// seed migration, settings and /api/core/v1/modules are added by the rows
// that land storage, tasks, config and modulehttp.
func TestHelloEndToEnd(t *testing.T) {
	capture := logcapture.New(t)
	a, err := app.New(t.Context(), app.Options{
		AppID:    "hello",
		Logger:   capture.Logger(),
		R1Values: func() app.R1Values { return app.R1Values{Listen: "localhost:0"} },
	}, hello.New())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()

	addr := listening(t, capture, done)
	get := func(name string) (int, []byte) {
		t.Helper()
		resp, err := http.Get("http://" + addr + "/api/hello/v1/greeting?name=" + url.QueryEscape(name))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, b
	}
	code, body := get("Bruce Wayne")
	var g hello.Greeting
	if err := json.Unmarshal(body, &g); err != nil || code != http.StatusOK || g.Message != "Hello, Bruce Wayne" {
		t.Fatalf("greeting = %d %s, want 200 Hello, Bruce Wayne", code, body)
	}
	if code, body := get(""); code != http.StatusUnprocessableEntity {
		t.Fatalf("empty name = %d %s, want 422", code, body)
	}
	if mods := a.Modules(); len(mods) != 1 || mods[0].ID != hello.ID || !mods[0].Enabled {
		t.Fatalf("Modules() = %+v", mods)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run after cancel = %v, want nil", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

// listening waits for the kernel's "listening" record and returns its addr.
func listening(t *testing.T, c *logcapture.Capture, done <-chan error) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		for _, r := range c.Records() {
			if r.Message != "kernel: listening" {
				continue
			}
			var addr string
			r.Attrs(func(a slog.Attr) bool {
				if a.Key == "addr" {
					addr = a.Value.String()
				}
				return true
			})
			return addr
		}
		select {
		case err := <-done:
			t.Fatalf("Run returned before listening: %v", err)
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Fatal("the app never logged that it was listening")
	return ""
}
