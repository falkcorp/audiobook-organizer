// file: cmd/fp_worker_test.go
// version: 1.0.0
// guid: 7e2d4c1a-9b3f-4f6e-a8d0-5c2b1e9f4a73
// last-edited: 2026-09-19

package cmd

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/viper"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint/workerclient"
)

// fp-worker runs on a Mac in whatever directory it is started from: the
// root command's app setup (playlists/ and database directories, the
// ~/.audiobook-organizer.yaml config, viper.AutomaticEnv) must not run.
func TestFPWorker_SkipsAppInitSideEffects(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	t.Setenv("HOME", t.TempDir())
	origDB, origPl, origCfg, origApp := databasePath, playlistDir, cfgFile, config.AppConfig
	t.Cleanup(func() {
		databasePath, playlistDir, cfgFile, config.AppConfig = origDB, origPl, origCfg, origApp
		viper.Reset()
		rootCmd.SetArgs(nil)
	})
	databasePath, playlistDir, cfgFile = "dbdir/audiobooks.pebble", "playlists", ""

	rootCmd.SetArgs([]string{"fp-worker"}) // fails fast: --server is required
	rootCmd.SetOut(io.Discard)
	rootCmd.SetErr(io.Discard)
	if err := rootCmd.Execute(); err == nil {
		t.Fatal("fp-worker without --server succeeded")
	}
	ents, err := os.ReadDir(cwd)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		t.Errorf("fp-worker created %q in the working directory", e.Name())
	}
}

// The bearer key must never follow a redirect to another origin.
func TestFPWorkerHTTPClient_NeverFollowsRedirects(t *testing.T) {
	var hit atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit.Add(1)
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/steal", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()

	hc, err := fpWorkerHTTPClient("")
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/fingerprint/worker/lease", bytes.NewReader([]byte("{}")))
	req.Header.Set("Authorization", "Bearer k")
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTemporaryRedirect {
		t.Errorf("status %d, want the 307 itself", resp.StatusCode)
	}
	if hit.Load() != 0 {
		t.Error("the redirect was followed")
	}
}

func TestFPWorkerAPIKey_UnsetsEnv(t *testing.T) {
	t.Setenv(workerclient.KeyEnvVar, "env-key-5b2e")
	k, err := fpWorkerAPIKey("")
	if err != nil || k != "env-key-5b2e" {
		t.Fatalf("fpWorkerAPIKey = %q, %v", k, err)
	}
	if v, ok := os.LookupEnv(workerclient.KeyEnvVar); ok {
		t.Errorf("%s still set (%q): child processes would inherit it", workerclient.KeyEnvVar, v)
	}
}

// First signal drains; the second cancels and, if Run is stuck (a hung hard
// NFS mount leaves goroutines in Lstat or D-state ffmpeg), exits after the
// grace period regardless.
func TestFPWorkerSignals_SecondSignalExitsAfterGrace(t *testing.T) {
	sigs := make(chan os.Signal, 2)
	drain := make(chan struct{})
	done := make(chan struct{}) // Run never returns
	var cancelled atomic.Bool
	exited := make(chan int, 1)
	go fpWorkerSignals(sigs, drain, done, func() { cancelled.Store(true) }, 20*time.Millisecond,
		func(code int) { exited <- code }, io.Discard)

	sigs <- syscall.SIGTERM
	select {
	case <-drain:
	case <-time.After(5 * time.Second):
		t.Fatal("first signal did not drain")
	}
	select {
	case code := <-exited:
		t.Fatalf("exited (%d) on the first signal", code)
	case <-time.After(50 * time.Millisecond):
	}
	if cancelled.Load() {
		t.Fatal("first signal cancelled the hard context")
	}
	sigs <- syscall.SIGINT
	select {
	case code := <-exited:
		if code == 0 {
			t.Errorf("hard exit code 0, want non-zero")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second signal never forced an exit while Run was stuck")
	}
	if !cancelled.Load() {
		t.Error("second signal did not cancel the hard context")
	}
}

// When Run returns on its own, no forced exit happens.
func TestFPWorkerSignals_NoExitAfterRunReturns(t *testing.T) {
	sigs := make(chan os.Signal, 2)
	drain := make(chan struct{})
	done := make(chan struct{})
	exited := make(chan int, 1)
	go fpWorkerSignals(sigs, drain, done, func() {}, 20*time.Millisecond, func(code int) { exited <- code }, io.Discard)
	sigs <- syscall.SIGTERM
	<-drain
	close(done)
	time.Sleep(20 * time.Millisecond)
	sigs <- syscall.SIGTERM
	select {
	case code := <-exited:
		t.Fatalf("forced exit (%d) after Run had returned", code)
	case <-time.After(100 * time.Millisecond):
	}
}
