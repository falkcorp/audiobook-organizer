// file: internal/tools/ollama_daemon.go
// version: 1.0.1
// guid: f2a3b4c5-d6e7-8901-fabc-901234567890
// last-edited: 2026-06-15

package tools

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// OllamaDaemonConfig holds constructor parameters for OllamaDaemon.
type OllamaDaemonConfig struct {
	BinPath      string // absolute path to ollama binary
	PIDFile      string // e.g. /var/lib/audiobook-organizer/tools/ollama.pid
	Port         int    // default 11434
	ReadyTimeout int    // milliseconds to wait for health check; default 15000
}

// OllamaDaemon supervises an Ollama child process: start on demand, adopt
// across server restarts via PID file, stop when idle.
type OllamaDaemon struct {
	cfg         OllamaDaemonConfig
	mu          sync.Mutex
	cmd         *exec.Cmd
	intentional bool
}

// NewOllamaDaemon creates an OllamaDaemon. Call EnsureRunningOrAdopt to start.
func NewOllamaDaemon(cfg OllamaDaemonConfig) *OllamaDaemon {
	if cfg.Port == 0 {
		cfg.Port = 11434
	}
	if cfg.ReadyTimeout == 0 {
		cfg.ReadyTimeout = 15000
	}
	return &OllamaDaemon{cfg: cfg}
}

// EnsureRunningOrAdopt starts Ollama or adopts a live process from the PID file.
// Returns nil if Ollama is ready (health check passes or adoption succeeded).
func (d *OllamaDaemon) EnsureRunningOrAdopt(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if pid, err := d.readPID(); err == nil {
		if processAlive(pid) {
			slog.Info("ollama: adopted existing process", "pid", pid)
			return nil
		}
		slog.Info("ollama: stale PID file, removing", "pid", pid)
		os.Remove(d.cfg.PIDFile)
	}

	cmd := exec.CommandContext(ctx, d.cfg.BinPath, "serve")
	cmd.Env = append(os.Environ(), fmt.Sprintf("OLLAMA_HOST=127.0.0.1:%d", d.cfg.Port))
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("ollama: start: %w", err)
	}
	d.cmd = cmd
	d.intentional = false

	if err := os.WriteFile(d.cfg.PIDFile, []byte(strconv.Itoa(cmd.Process.Pid)), 0o644); err != nil {
		slog.Warn("ollama: could not write PID file", "err", err)
	}

	go d.supervise(cmd)

	if err := d.waitReady(ctx, time.Duration(d.cfg.ReadyTimeout)*time.Millisecond); err != nil {
		// Synchronous cleanup: kill the child and remove the PID file before
		// returning so the caller sees a clean state and tests are not racy.
		d.intentional = true // suppress supervise()'s "exited unexpectedly" warning
		if d.cmd != nil && d.cmd.Process != nil {
			d.cmd.Process.Kill()
		}
		os.Remove(d.cfg.PIDFile)
		return err
	}
	return nil
}

// StopWhenIdle sends SIGTERM to the Ollama process and waits for clean exit.
func (d *OllamaDaemon) StopWhenIdle(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.intentional = true

	pid, err := d.readPID()
	if err != nil {
		return nil
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		os.Remove(d.cfg.PIDFile)
		return nil
	}

	if err := proc.Signal(syscall.SIGTERM); err != nil {
		os.Remove(d.cfg.PIDFile)
		return nil
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		if d.cmd != nil {
			d.cmd.Wait()
		} else {
			for i := 0; i < 100; i++ {
				time.Sleep(100 * time.Millisecond)
				if !processAlive(pid) {
					return
				}
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		proc.Signal(syscall.SIGKILL)
	case <-ctx.Done():
		proc.Signal(syscall.SIGKILL)
	}

	os.Remove(d.cfg.PIDFile)
	slog.Info("ollama: stopped")
	return nil
}

func (d *OllamaDaemon) supervise(cmd *exec.Cmd) {
	cmd.Wait()
	d.mu.Lock()
	intentional := d.intentional
	d.mu.Unlock()
	if intentional {
		return
	}
	slog.Warn("ollama: process exited unexpectedly; will restart on next EnsureRunningOrAdopt call")
	os.Remove(d.cfg.PIDFile)
}

func (d *OllamaDaemon) waitReady(ctx context.Context, timeout time.Duration) error {
	url := fmt.Sprintf("http://127.0.0.1:%d/api/tags", d.cfg.Port)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		resp, err := http.DefaultClient.Do(req)
		if err == nil && resp.StatusCode == http.StatusOK {
			resp.Body.Close()
			slog.Info("ollama: ready", "port", d.cfg.Port)
			return nil
		}
		if resp != nil {
			resp.Body.Close()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return errors.New("ollama: health check timed out")
}

func (d *OllamaDaemon) readPID() (int, error) {
	b, err := os.ReadFile(d.cfg.PIDFile)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(b)))
}

func processAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}
