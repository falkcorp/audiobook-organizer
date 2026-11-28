// file: cmd/boot_inputs_test.go
// version: 1.0.0
// guid: 0b7e5d14-93a6-4c28-b1f0-4d86a2e7c915
// last-edited: 2026-11-20

package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// rootLikeFlags is a flag set shaped like the root command's persistent flags,
// with every flag at its default.
func rootLikeFlags() *pflag.FlagSet {
	fs := pflag.NewFlagSet("root", pflag.ContinueOnError)
	fs.String("config", "", "")
	fs.String("db", "audiobooks.pebble", "")
	fs.String("log-level", "info", "")
	return fs
}

// A flag left at its default is not a layer: --db's default is a relative name
// HEAD resolved against the working directory, and a layer would lock the key
// against the environment.
func TestBootInputs_DefaultsContributeNothing(t *testing.T) {
	in, err := bootInputs(rootLikeFlags())
	require.NoError(t, err)
	assert.Empty(t, in.Flags)
	assert.Empty(t, in.ConfigFile)
}

// Only the flags the operator set become layers, --db is made absolute against
// the working directory, and --config names the operator file.
func TestBootInputs_OnlyChangedFlagsAndDBIsAbsolute(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	fs := rootLikeFlags()
	require.NoError(t, fs.Parse([]string{"--db", "gotham.pebble", "--config", "op.yaml"}))
	in, err := bootInputs(fs)
	require.NoError(t, err)
	want, err := filepath.Abs("gotham.pebble")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"core.storage.path": want}, stringMap(in.Flags), "log-level was not set and must not appear")
	assert.Equal(t, "op.yaml", in.ConfigFile)

	fs = rootLikeFlags()
	require.NoError(t, fs.Parse([]string{"--log-level", "debug"}))
	in, err = bootInputs(fs)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"core.log.level": "debug"}, stringMap(in.Flags))
}

// The op-runner child follows the parent's resolved store exactly, whatever the
// child's own environment says, and a child handed no path leaves the layers
// alone.
func TestResolveChildPaths_FollowsTheParentsStore(t *testing.T) {
	restoreRootFlags(t)
	state := filepath.Join(t.TempDir(), "state")
	require.NoError(t, os.MkdirAll(state, 0o700))
	t.Setenv(config.SecureStateDirEnv, state)
	config.ResetStateDirForTest()
	t.Cleanup(config.ResetStateDirForTest)
	t.Chdir(t.TempDir())
	t.Setenv("HOME", t.TempDir())

	parent := filepath.Join(t.TempDir(), "gotham.pebble")
	t.Setenv("ABO_CORE_STORAGE_PATH", filepath.Join(t.TempDir(), "elsewhere.pebble"))
	db, dir, err := resolveChildPaths(parent)
	require.NoError(t, err)
	assert.Equal(t, parent, db, "the child opened a store other than its parent's")
	assert.Equal(t, state, dir)

	env := filepath.Join(t.TempDir(), "from-env.pebble")
	t.Setenv("ABO_CORE_STORAGE_PATH", env)
	db, _, err = resolveChildPaths("")
	require.NoError(t, err)
	assert.Equal(t, env, db, "a child with no parent path must follow the layers")
}

func stringMap[K ~string](m map[K]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[string(k)] = v
	}
	return out
}

// serveLikeFlags is a flag set shaped like serve's own listener flags, at
// their defaults.
func serveLikeFlags() *pflag.FlagSet {
	fs := pflag.NewFlagSet("serve", pflag.ContinueOnError)
	fs.String("port", "8484", "")
	fs.String("host", "localhost", "")
	fs.String("tls-cert", "", "")
	fs.String("tls-key", "", "")
	return fs
}

// serve's listener flags are flag layers on their canonical keys, set only
// when the operator passed them: --host and --port make one core.http.listen,
// the flag the operator left alone keeping its default, and --tls-cert and
// --tls-key name core.http.tls.cert_file and key_file. With none passed there
// is no layer, so the environment and the stored value decide.
func TestServeBootInputs_ListenerFlagsAreLayers(t *testing.T) {
	restoreRootFlags(t)
	for _, tc := range []struct {
		name string
		args []string
		want map[string]string
	}{
		{name: "none passed", want: map[string]string{}},
		{name: "port only", args: []string{"--port", "9000"}, want: map[string]string{"core.http.listen": "localhost:9000"}},
		{name: "host only", args: []string{"--host", "192.0.2.5"}, want: map[string]string{"core.http.listen": "192.0.2.5:8484"}},
		{name: "both, IPv6 host", args: []string{"--host", "::1", "--port", "9443"}, want: map[string]string{"core.http.listen": "[::1]:9443"}},
		{name: "tls pair", args: []string{"--tls-cert", "/etc/wayne/batcave.crt", "--tls-key", "/etc/wayne/batcave.key"},
			want: map[string]string{"core.http.tls.cert_file": "/etc/wayne/batcave.crt", "core.http.tls.key_file": "/etc/wayne/batcave.key"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := serveLikeFlags()
			require.NoError(t, fs.Parse(tc.args))
			in, err := serveBootInputs(fs)
			require.NoError(t, err)
			assert.Equal(t, tc.want, stringMap(in.Flags))
		})
	}
}
