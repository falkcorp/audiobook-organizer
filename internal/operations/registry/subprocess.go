// file: internal/operations/registry/subprocess.go
// version: 1.2.0
// guid: 2b3c4d5e-6f7a-8901-bcde-f01234567890
// last-edited: 2026-06-22

// Package registry — subprocess runner for Isolate=true operations.
//
// # Protocol
//
// The parent re-execs itself with args ["--operation-runner", <opID>] and sets
// the environment variable UOS_SOCKET to a unix socket path. Over that socket:
//
//  1. Parent sends a newline-terminated JSON line: {"def_id":"...","params":{...}}
//  2. Child calls def.Run(ctx, params, reporter).
//  3. Child sends a newline-terminated JSON result: {"ok":true} or {"ok":false,"error":"..."}.
//  4. Child exits 0 (success) or 1 (failure).
//
// # Main.go wiring (UOS-03 note)
//
// For child mode to work, main.go must call registry.MaybeRunChildMode() as the
// very first thing after DB init. That wiring is deferred to UOS-04 because the
// child needs a fully initialised store. The child mode hook (RunChildMode) is
// exported here so UOS-04 can call it without additional registry changes.

package registry

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// childModeArg is the sentinel os.Args[1] value that triggers child mode.
const childModeArg = "--operation-runner"

// EnvSocketPath is the environment variable name for the unix socket path.
const EnvSocketPath = "UOS_SOCKET"

// Env variables propagated from parent to child so the child can open the
// same store and register the same plugin defs as the parent. The child
// side (cmd.RunOperationRunner) is the consumer of these.
const (
	EnvChildDBPath  = "UOS_DB_PATH"
	EnvChildDBType  = "UOS_DB_TYPE"
	EnvChildRootDir = "UOS_ROOT_DIR"
)

// ChildEnvFunc returns the additional KEY=VALUE strings (without the trailing
// newline) that should be appended to the child process's environment when
// the parent re-execs itself as an operation-runner. Production wires this
// to a closure over internal/config.AppConfig from cmd.init(). Tests may
// leave it nil — runSubprocess only forwards UOS_SOCKET in that case.
var ChildEnvFunc func() []string

// childHandshake is the JSON payload sent from parent to child.
type childHandshake struct {
	DefID  string          `json:"def_id"`
	Params json.RawMessage `json:"params"`
}

// childResult is the JSON payload sent from child to parent.
type childResult struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// IsChildMode returns true when this process was re-exec'd as a child runner.
// Call early in main() before any DB init if you want to fast-fail on unknown
// operations; otherwise call after DB init to get reporter persistence.
func IsChildMode() bool {
	return len(os.Args) >= 2 && os.Args[1] == childModeArg
}

// RunChildMode executes the operation runner child path.
// r must be a fully initialised Registry (with store). Call this only when
// IsChildMode() returns true. It runs the operation and calls os.Exit.
//
// This function never returns normally.
func RunChildMode(r *Registry) {
	if len(os.Args) < 3 {
		slog.Default().Error("child mode: missing opID argument")
		os.Exit(2)
	}
	opID := os.Args[2]

	socketPath := os.Getenv(EnvSocketPath)
	if socketPath == "" {
		slog.Default().Error("child mode: UOS_SOCKET not set", "op_id", opID)
		os.Exit(2)
	}

	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		slog.Default().Error("child mode: failed to connect to parent socket", "op_id", opID, "error", err)
		os.Exit(2)
	}
	defer conn.Close()

	// Receive handshake from parent.
	var hs childHandshake
	dec := json.NewDecoder(conn)
	if err := dec.Decode(&hs); err != nil {
		writeChildResult(conn, false, fmt.Sprintf("decode handshake: %v", err))
		os.Exit(1)
	}

	// Look up def.
	r.mu.RLock()
	def, ok := r.defs[hs.DefID]
	r.mu.RUnlock()
	if !ok {
		writeChildResult(conn, false, fmt.Sprintf("unknown def_id: %s", hs.DefID))
		os.Exit(1)
	}

	// Create reporter.
	ctx := context.Background()
	// Subprocess runs in a separate process; no runHandle to stamp, so
	// touchProgressFn is nil and synchronous mode preserves DB writes.
	reporter := newDBReporter(ctx, opID, def.ID, def.DisplayName, def.Plugin, "", "", r.store, nil, r.activityRecorder, r.logger, nil, nil, 0, true)

	// Run.
	runErr := def.Run(ctx, hs.Params, reporter)
	if runErr != nil {
		writeChildResult(conn, false, runErr.Error())
		os.Exit(1)
	}
	writeChildResult(conn, true, "")
	os.Exit(0)
}

// writeChildResult sends the result JSON over conn; errors are ignored (best-effort).
func writeChildResult(conn net.Conn, ok bool, errMsg string) {
	res := childResult{OK: ok, Error: errMsg}
	b, _ := json.Marshal(res)
	b = append(b, '\n')
	_, _ = conn.Write(b)
}

// runSubprocess re-execs the current binary with child mode args, manages the
// unix socket pair, routes stdout/stderr to the reporter, and waits for exit.
func runSubprocess(ctx context.Context, def OperationDef, opID string, params json.RawMessage, reporter Reporter) error {
	// Create a temp dir for the socket.
	tmpDir, err := os.MkdirTemp("", "uos-")
	if err != nil {
		return fmt.Errorf("subprocess: mkdirtemp: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	socketPath := filepath.Join(tmpDir, "s") // keep short for macOS 104-char limit
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("subprocess: listen unix: %w", err)
	}
	defer ln.Close()

	// Re-exec self.
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("subprocess: executable: %w", err)
	}

	cmd := exec.CommandContext(ctx, exe, childModeArg, opID)
	childEnv := append(os.Environ(), fmt.Sprintf("%s=%s", EnvSocketPath, socketPath))
	if ChildEnvFunc != nil {
		childEnv = append(childEnv, ChildEnvFunc()...)
	}
	cmd.Env = childEnv
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Capture stdout/stderr.
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("subprocess: stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("subprocess: stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("subprocess: start: %w", err)
	}

	// Route stdout/stderr to reporter.
	go routeLines(stdoutPipe, slog.LevelInfo, reporter)
	go routeLines(stderrPipe, slog.LevelWarn, reporter)

	// Accept the child connection. Use a shorter deadline (5s) so that if the
	// child exits immediately (e.g. in tests, or because it doesn't support
	// --operation-runner yet), we detect the failure quickly.
	connCh := make(chan net.Conn, 1)
	acceptErrCh := make(chan error, 1)
	go func() {
		_ = ln.(*net.UnixListener).SetDeadline(time.Now().Add(5 * time.Second))
		conn, err := ln.Accept()
		if err != nil {
			acceptErrCh <- fmt.Errorf("subprocess: accept: %w", err)
			return
		}
		connCh <- conn
	}()

	// Also wait for the child to exit so we fail fast if it exits without connecting.
	childExitCh := make(chan error, 1)
	go func() { childExitCh <- cmd.Wait() }()

	var conn net.Conn
	select {
	case conn = <-connCh:
		// Child connected; don't call cmd.Wait() yet — do it after result read.
	case err := <-acceptErrCh:
		<-childExitCh
		return err
	case childExitErr := <-childExitCh:
		_ = ln.Close()
		if childExitErr != nil {
			return fmt.Errorf("subprocess: child exited without connecting: %w", childExitErr)
		}
		return fmt.Errorf("subprocess: child exited cleanly without connecting to socket")
	case <-ctx.Done():
		_ = killProcess(cmd, 0)
		<-childExitCh
		return ctx.Err()
	}
	defer conn.Close()

	// Send handshake.
	hs := childHandshake{DefID: def.ID, Params: params}
	b, _ := json.Marshal(hs)
	b = append(b, '\n')
	if _, err := conn.Write(b); err != nil {
		_ = killProcess(cmd, 0)
		<-childExitCh
		return fmt.Errorf("subprocess: write handshake: %w", err)
	}

	// Read result with context cancellation support.
	type readResult struct {
		res childResult
		err error
	}
	readCh := make(chan readResult, 1)
	go func() {
		scanner := bufio.NewScanner(conn)
		if scanner.Scan() {
			var res childResult
			if err := json.Unmarshal(scanner.Bytes(), &res); err != nil {
				readCh <- readResult{err: fmt.Errorf("subprocess: unmarshal result: %w", err)}
				return
			}
			readCh <- readResult{res: res}
		} else {
			if err := scanner.Err(); err != nil {
				readCh <- readResult{err: fmt.Errorf("subprocess: read result: %w", err)}
			} else {
				readCh <- readResult{err: fmt.Errorf("subprocess: child closed connection without sending result")}
			}
		}
	}()

	var childErr error
	select {
	case rr := <-readCh:
		if rr.err != nil {
			childErr = rr.err
		} else if !rr.res.OK {
			childErr = fmt.Errorf("subprocess op failed: %s", rr.res.Error)
		}
	case <-ctx.Done():
		// Cancel child: SIGTERM then SIGKILL.
		_ = killProcess(cmd, syscall.SIGTERM)
		select {
		case <-time.After(10 * time.Second):
			_ = killProcess(cmd, syscall.SIGKILL)
		case <-readCh:
		}
		childErr = ctx.Err()
	}

	// Wait for child to fully exit (it should have exited already or shortly).
	<-childExitCh
	return childErr
}

// routeLines reads lines from r and logs each one to the reporter.
func routeLines(r io.Reader, level slog.Level, reporter Reporter) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		_ = reporter.Log(level, line)
	}
}

// killProcess sends sig to the process group of cmd.
func killProcess(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd.Process == nil {
		return nil
	}
	if sig == 0 {
		sig = syscall.SIGTERM
	}
	// Negative PID targets the process group (Setpgid=true above).
	return syscall.Kill(-cmd.Process.Pid, sig)
}
