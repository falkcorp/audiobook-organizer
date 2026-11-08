// file: core/kernel/logfile_test.go
// version: 1.0.0
// guid: 5e0b7f3a-2c4d-4a61-9b8e-71d3c2f40a96
// last-edited: 2026-11-08

package kernel

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/log"
)

// teeBuffer is a bytes.Buffer safe for the pipeline's concurrent writes.
type teeBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *teeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *teeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// fileSettings is core.log with the file output on, as ABO's books module
// sets it.
func fileSettings() *log.Settings {
	s := log.DefaultSettings()
	s.Output = "file"
	s.File.Path = "logs/gotham.log"
	return &s
}

// TestLogOutput_FileUnderStateDir: with output=file the live file is created
// under the state directory (05-logging §4 step 1) and receives every record,
// the tee receives them too while tee_stderr is on, and a record logged after
// closeCore reaches the tee without counting as lost.
func TestLogOutput_FileUnderStateDir(t *testing.T) {
	dir := t.TempDir()
	tee := &teeBuffer{}
	k, err := New(t.Context(), Config{AppID: "gotham", LogDir: dir, LogOutput: tee, Log: fileSettings()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	k.RootLogger().Info("the signal is lit")
	got, err := os.ReadFile(filepath.Join(dir, "logs", "gotham.log"))
	if err != nil {
		t.Fatalf("no log file under the state directory: %v", err)
	}
	if !strings.Contains(string(got), "the signal is lit") {
		t.Errorf("the file lacks the record: %q", got)
	}
	if !strings.Contains(tee.String(), "the signal is lit") {
		t.Errorf("the tee lacks the record: %q", tee.String())
	}

	if err := k.closeCore(context.Background(), time.Now().Add(defaultShutdownBudget)); err != nil {
		t.Fatalf("closeCore: %v", err)
	}
	k.RootLogger().Info("after the shutdown")
	if !strings.Contains(tee.String(), "after the shutdown") {
		t.Errorf("a record logged after closeCore did not reach the tee: %q", tee.String())
	}
	if n := k.logging.Counters().Dropped["primary"]; n != 0 {
		t.Errorf("a record logged after closeCore was counted lost (%d)", n)
	}
}

func TestLogOutput_NoTeeWhenOff(t *testing.T) {
	dir := t.TempDir()
	tee := &teeBuffer{}
	s := fileSettings()
	s.File.TeeStderr = false
	k := mustNew(t, Config{AppID: "gotham", LogDir: dir, LogOutput: tee, Log: s})
	k.RootLogger().Info("only in the file")
	if tee.String() != "" {
		t.Errorf("tee_stderr is off, yet the tee got %q", tee.String())
	}
	got, _ := os.ReadFile(filepath.Join(dir, "logs", "gotham.log"))
	if !strings.Contains(string(got), "only in the file") {
		t.Errorf("the file lacks the record: %q", got)
	}
}

// TestLogOutput_DefaultPathIsAppID: an empty path defaults to logs/<AppID>.log.
func TestLogOutput_DefaultPathIsAppID(t *testing.T) {
	dir := t.TempDir()
	s := fileSettings()
	s.File.Path = ""
	k := mustNew(t, Config{AppID: "gotham", LogDir: dir, LogOutput: &teeBuffer{}, Log: s})
	k.RootLogger().Info("x")
	if _, err := os.Stat(filepath.Join(dir, "logs", "gotham.log")); err != nil {
		t.Errorf("the default path logs/<app-id>.log was not used: %v", err)
	}
}

// TestLogOutput_LevelAppliesAndBadSettingsFailBoot: the level of Config.Log
// is the handler's level; an invalid level, format or output, a file with no
// state directory, and a state directory that cannot be opened each fail boot
// and leave no file behind (05-logging §6).
func TestLogOutput_LevelAppliesAndBadSettingsFailBoot(t *testing.T) {
	var out teeBuffer
	s := log.DefaultSettings()
	s.Level = "debug"
	k := mustNew(t, Config{AppID: "gotham", LogOutput: &out, Log: &s})
	k.RootLogger().Debug("detective mode")
	if !strings.Contains(out.String(), "detective mode") {
		t.Errorf("level=debug did not reach the handler: %q", out.String())
	}

	dir := t.TempDir()
	for name, tc := range map[string]struct {
		mut  func(*log.Settings)
		dir  string
		want string
	}{
		"level":    {func(s *log.Settings) { s.Level = "loud" }, dir, "core.log.level"},
		"format":   {func(s *log.Settings) { s.Format = "xml" }, dir, "core.log.format"},
		"output":   {func(s *log.Settings) { s.Output = "syslog" }, dir, "core.log.output"},
		"no dir":   {func(s *log.Settings) { s.Output = "file" }, "", "state directory"},
		"bad dir":  {func(s *log.Settings) { s.Output = "file" }, filepath.Join(dir, "missing"), "state directory"},
		"bad path": {func(s *log.Settings) { s.Output = "file"; s.File.Path = "../out.log" }, dir, "core.log.file"},
	} {
		t.Run(name, func(t *testing.T) {
			s := log.DefaultSettings()
			tc.mut(&s)
			_, err := New(t.Context(), Config{AppID: "gotham", LogDir: tc.dir, LogOutput: &teeBuffer{}, Log: &s})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("New error = %v, want one naming %q", err, tc.want)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(dir, "out.log")); err == nil {
		t.Error("a refused path wrote outside the state directory")
	}
}

// TestLogOutput_DescribeOpensNoFile: Describe discards its log output and
// touches no file, whatever the settings say (COR2-13).
func TestLogOutput_DescribeOpensNoFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := Describe(t.Context(), Config{AppID: "gotham", LogDir: dir, Log: fileSettings()}); err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("Describe left %d entries under the state directory", len(entries))
	}
}

// TestKernelLogging_Accessor: the kernel exposes its pipeline for taps, and
// nil when it adopted Config.Logger.
func TestKernelLogging_Accessor(t *testing.T) {
	k := mustNew(t, Config{AppID: "gotham", LogOutput: &teeBuffer{}})
	if k.Logging() == nil {
		t.Fatal("Logging() = nil for a kernel that built its pipeline")
	}
}
