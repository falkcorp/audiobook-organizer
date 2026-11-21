// file: internal/books/cutover/deployfiles_test.go
// version: 1.0.0
// guid: 0e521908-0e3b-400f-a678-3669e4b04d98
// last-edited: 2026-11-18

package cutover

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// Deploy files (05-config §1.12.4, F3-07 item 7): every environment variable a
// shipped deploy file sets, commented examples included, is a canonical name,
// a kept ecosystem binding (both in CanonicalEnvNames), a variable the Go
// runtime or the OS reads, or a process name that is kept. A retired name, or
// any other name, fails with the file, line and name.

// deployFileRuntimeNames are read by the Go runtime, the systemd notify helper
// or the OS, never by config.
var deployFileRuntimeNames = []string{
	"HOME", "GOMEMLIMIT", "GOGC", "TZ", "NOTIFY_SOCKET", "TMPDIR", "SQLITE_TMPDIR",
}

// deployFileKeptProcessNames are process names the app keeps reading.
var deployFileKeptProcessNames = []string{"OTEL_SERVICE_NAME", "ABK_PPROF_ADDR"}

// deployFileCoreNativeNames are canonical names of core-native keys HEAD never
// had a name for, so the rename table (and CanonicalEnvNames) does not list
// them: core.shutdown.budget, read by cmd/root.go as ABO_CORE_SHUTDOWN_BUDGET.
var deployFileCoreNativeNames = []string{"ABO_CORE_SHUTDOWN_BUDGET"}

// deployFileNotifyWords are sd_notify protocol words (READY=1, STOPPING=1,
// STATUS=...) the unit's prose names at the start of a comment line; they are
// messages to systemd, never environment variables.
var deployFileNotifyWords = []string{"READY", "STOPPING", "STATUS"}

type deployFormat int

const (
	formatSystemd    deployFormat = iota // Environment= lines and "# NAME=value" examples
	formatPlist                          // <key> entries of the EnvironmentVariables dict
	formatCompose                        // entries of an environment: block
	formatDockerfile                     // ENV instructions
)

// deployFiles are the shipped files scanned, relative to the repo root.
var deployFiles = []struct {
	path   string
	format deployFormat
}{
	{"deploy/audiobook-organizer.service", formatSystemd},
	{"deploy/local.conf.example", formatSystemd},
	{"deploy/com.audiobook-organizer.plist", formatPlist},
	{"deploy/launchd/com.jdfalk.audiobook-organizer.plist", formatPlist},
	{"docker-compose.yml", formatCompose},
	{"Dockerfile", formatDockerfile},
	{"Dockerfile.build-cgo", formatDockerfile},
}

type envUse struct {
	line int
	name string
}

var (
	envNameRE       = `[A-Za-z_][A-Za-z0-9_]*`
	systemdAssignRE = regexp.MustCompile(`(?:^|[\s"'])(` + envNameRE + `)=`)
	systemdEnvRE    = regexp.MustCompile(`^#*\s*Environment=(.*)$`)
	systemdBareRE   = regexp.MustCompile(`^#+\s*([A-Z][A-Z0-9_]*)=`)
	plistKeyRE      = regexp.MustCompile(`<key>(` + envNameRE + `)</key>`)
	composeItemRE   = regexp.MustCompile(`^\s*#*\s*(?:-\s*)?["']?(` + envNameRE + `)["']?\s*(?:[=:]|$)`)
	dockerEnvRE     = regexp.MustCompile(`^\s*ENV\s+(.*)$`)
	dockerPairRE    = regexp.MustCompile(`(?:^|\s)(` + envNameRE + `)=`)
)

// scanDeployEnv returns every environment variable text sets, in file format f.
func scanDeployEnv(f deployFormat, text string) []envUse {
	var out []envUse
	inEnv, envIndent := false, 0 // plist dict / compose block state
	for i, line := range strings.Split(text, "\n") {
		n := i + 1
		add := func(name string) { out = append(out, envUse{n, name}) }
		switch f {
		case formatSystemd:
			if m := systemdEnvRE.FindStringSubmatch(line); m != nil {
				for _, a := range systemdAssignRE.FindAllStringSubmatch(" "+m[1], -1) {
					add(a[1])
				}
			} else if m := systemdBareRE.FindStringSubmatch(line); m != nil {
				add(m[1])
			}
		case formatPlist:
			if strings.Contains(line, "<key>EnvironmentVariables</key>") {
				inEnv = true
				continue
			}
			if inEnv && strings.Contains(line, "</dict>") {
				inEnv = false
			}
			if inEnv {
				for _, m := range plistKeyRE.FindAllStringSubmatch(line, -1) {
					add(m[1])
				}
			}
		case formatCompose:
			body := strings.TrimLeft(strings.TrimLeft(line, " "), "#")
			indent := len(line) - len(strings.TrimLeft(line, " "))
			if strings.TrimSpace(body) == "environment:" {
				inEnv, envIndent = true, indent
				continue
			}
			if inEnv && strings.TrimSpace(line) != "" && !strings.HasPrefix(strings.TrimSpace(line), "#") && indent <= envIndent {
				inEnv = false
			}
			if inEnv {
				if m := composeItemRE.FindStringSubmatch(line); m != nil {
					add(m[1])
				}
			}
		case formatDockerfile:
			if m := dockerEnvRE.FindStringSubmatch(line); m != nil {
				if pairs := dockerPairRE.FindAllStringSubmatch(" "+m[1], -1); pairs != nil {
					for _, p := range pairs {
						add(p[1])
					}
				} else if fs := strings.Fields(m[1]); len(fs) > 0 { // ENV NAME value
					add(fs[0])
				}
			}
		}
	}
	return out
}

// deployEnvViolations returns one message per variable in uses that is not allowed.
func deployEnvViolations(file string, uses []envUse, allowed map[string]bool) []string {
	var bad []string
	for _, u := range uses {
		if !allowed[u.name] {
			bad = append(bad, fmt.Sprintf("%s:%d: %s is not a canonical, ecosystem, runtime or kept name", file, u.line, u.name))
		}
	}
	return bad
}

func allowedDeployEnvNames() map[string]bool {
	allowed := map[string]bool{}
	for _, n := range slices.Concat(CanonicalEnvNames(), deployFileRuntimeNames, deployFileKeptProcessNames, deployFileCoreNativeNames, deployFileNotifyWords) {
		allowed[n] = true
	}
	return allowed
}

// repoRoot walks up from the working directory to go.mod (not runtime.Caller:
// CI builds with -trimpath).
func deployRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the working directory")
		}
		dir = parent
	}
}

func TestDeployFilesUseCanonicalNamesOnly(t *testing.T) {
	root := deployRepoRoot(t)
	allowed := allowedDeployEnvNames()
	for _, f := range deployFiles {
		t.Run(f.path, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join(root, f.path))
			if err != nil {
				t.Fatal(err)
			}
			for _, msg := range deployEnvViolations(f.path, scanDeployEnv(f.format, string(b)), allowed) {
				t.Error(msg)
			}
		})
	}
}

// TestDeployComposePassesBasicGateOnlyWhenSet: compose passes the Basic gate's
// variables as bare names (a host value passes through only when set), never
// with a value or a `${VAR:-default}`, so a default install sets no
// ABO_CORE_HTTP_BASIC_GATE_ENABLED=false and a gate enabled in Settings stays on.
func TestDeployComposePassesBasicGateOnlyWhenSet(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(deployRepoRoot(t), "docker-compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	valued := regexp.MustCompile(`ABO_CORE_HTTP_BASIC_GATE_[A-Z_]+\s*[=:]`)
	for i, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if valued.MatchString(line) {
			t.Errorf("docker-compose.yml:%d gives a Basic-gate variable a value: %s", i+1, strings.TrimSpace(line))
		}
	}
}

// TestDeployFilesScannerCatchesRetiredNames proves the scanner sees a retired
// name in every file format, in live and commented form, and does not flag
// canonical, runtime or kept names or prose.
func TestDeployFilesScannerCatchesRetiredNames(t *testing.T) {
	allowed := allowedDeployEnvNames()
	const retired = "DATABASE_PATH"
	cases := []struct {
		name   string
		format deployFormat
		text   string
	}{
		{"systemd live", formatSystemd, "[Service]\nEnvironment=\"" + retired + "=/x\"\n"},
		{"systemd unquoted", formatSystemd, "Environment=" + retired + "=/x\n"},
		{"systemd second assignment", formatSystemd, "Environment=\"GOGC=200\" \"" + retired + "=/x\"\n"},
		{"systemd commented Environment", formatSystemd, "#Environment=" + retired + "=/x\n"},
		{"systemd commented bare", formatSystemd, "# " + retired + "=/x\n"},
		{"plist live", formatPlist, "<key>EnvironmentVariables</key>\n<dict>\n<key>" + retired + "</key>\n<string>/x</string>\n</dict>\n"},
		{"plist commented", formatPlist, "<key>EnvironmentVariables</key>\n<dict>\n<!-- <key>" + retired + "</key> -->\n</dict>\n"},
		{"compose list", formatCompose, "services:\n  a:\n    environment:\n      - " + retired + "=/x\n"},
		{"compose bare", formatCompose, "services:\n  a:\n    environment:\n      - " + retired + "\n"},
		{"compose map", formatCompose, "services:\n  a:\n    environment:\n      " + retired + ": /x\n"},
		{"compose commented", formatCompose, "services:\n  a:\n    environment:\n      # - " + retired + "=/x\n"},
		{"dockerfile pair", formatDockerfile, "FROM scratch\nENV " + retired + "=/x\n"},
		{"dockerfile second pair", formatDockerfile, "ENV GOGC=200 " + retired + "=/x\n"},
		{"dockerfile space form", formatDockerfile, "ENV " + retired + " /x\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bad := deployEnvViolations("x", scanDeployEnv(c.format, c.text), allowed)
			if len(bad) != 1 || !strings.Contains(bad[0], retired) {
				t.Fatalf("want exactly one violation naming %s, got %v", retired, bad)
			}
		})
	}

	clean := []struct {
		format deployFormat
		text   string
	}{
		{formatSystemd, "# Environment variables go in the drop-in\nEnvironment=\"HOME=/x\"\n#Environment=ABO_CORE_STORAGE_PATH=/x\n# ExecStart=/bin/x --db /p\n"},
		{formatPlist, "<key>Label</key>\n<key>EnvironmentVariables</key>\n<dict>\n<key>ABO_CORE_STORAGE_PATH</key>\n</dict>\n<key>RunAtLoad</key>\n"},
		{formatCompose, "services:\n  a:\n    environment:\n      - ABO_CORE_HTTP_BASIC_GATE_ENABLED\n    restart: x\n    volumes:\n      - " + retired + ":/y\n"},
		{formatDockerfile, "ARG " + retired + "\nRUN echo ${" + retired + "}\nENV TZ=UTC OTEL_SERVICE_NAME=x\n"},
	}
	for i, c := range clean {
		if bad := deployEnvViolations("x", scanDeployEnv(c.format, c.text), allowed); len(bad) != 0 {
			t.Errorf("clean case %d flagged: %v", i, bad)
		}
	}
}
