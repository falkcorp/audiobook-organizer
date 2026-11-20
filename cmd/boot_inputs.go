// file: cmd/boot_inputs.go
// version: 1.0.0
// guid: 9a4c2e71-6b05-4d83-a1f7-3e8d5c0b9264
// last-edited: 2026-11-20

package cmd

import (
	"context"
	"fmt"
	"net"
	"path/filepath"

	"github.com/spf13/pflag"

	"github.com/falkcorp/audiobook-organizer/internal/books"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	coreconfig "github.com/falkcorp/mediacore/config"
)

// flagConfigPaths maps each root persistent flag that names a core config key
// onto its canonical path (05-config section 1.12.4 "Flags"). --dir and
// --playlists are not here: their settings are blob keys under
// core.library.roots and the playlist directory, not scalar core keys, so
// they feed the commands that read them, not the layered store. --config is
// core.config.file and --db-type is checked and dropped (bootInputs).
var flagConfigPaths = map[string]coreconfig.Path{
	"db":        "core.storage.path",
	"log-level": "core.log.level",
}

// bootInputs is books.BootInputs for the root flags in fs: only a flag the
// operator set explicitly, as config.FromFlagSet's rule has it. A flag left at
// its default must not become a layer: it would lock the key against the
// environment and the stored value, and --db's default is a relative
// "audiobooks.pebble" that HEAD resolved against the working directory.
//
// --db is made absolute here, against the working directory, as HEAD's open
// did, because core refuses a relative store path. --db-type other than
// pebble is refused as HEAD refused it: no other backend exists.
func bootInputs(fs *pflag.FlagSet) (books.BootInputs, error) {
	in := books.BootInputs{}
	for name, path := range flagConfigPaths {
		f := fs.Lookup(name)
		if f == nil || !f.Changed {
			continue
		}
		v := f.Value.String()
		if name == "db" {
			abs, err := filepath.Abs(v)
			if err != nil {
				return books.BootInputs{}, fmt.Errorf("--db %q: %w", v, err)
			}
			v = abs
		}
		if in.Flags == nil {
			in.Flags = map[coreconfig.Path]string{}
		}
		in.Flags[path] = v
	}
	if f := fs.Lookup("config"); f != nil && f.Changed {
		in.ConfigFile = f.Value.String()
	}
	if f := fs.Lookup("db-type"); f != nil && f.Changed {
		if err := database.ValidateDatabaseType(f.Value.String()); err != nil {
			return books.BootInputs{}, err
		}
	}
	return in, nil
}

// serveFlagConfigPaths maps serve's own flags that name a core key onto its
// canonical path. --host and --port are one key between them
// (core.http.listen), so they are handled by serveBootInputs, not here.
var serveFlagConfigPaths = map[string]coreconfig.Path{
	"tls-cert": "core.http.tls.cert_file",
	"tls-key":  "core.http.tls.key_file",
}

// serveBootInputs is bootInputs for serve: the root flags (persistent, on
// the root command) and serve's own, in the same changed-only form. --host
// and --port make one flag layer value for core.http.listen, the host:port
// they name, with the flag's own default standing in for the one the operator
// did not set (--port 9000 listens on localhost:9000, as it always did).
// A flag left at its default is no layer, so it locks nothing: the
// environment (ABO_CORE_HTTP_LISTEN) and the stored value decide, and with
// neither core.http.listen is ABO's compiled default (localhost:8484).
func serveBootInputs(fs *pflag.FlagSet) (books.BootInputs, error) {
	in, err := rootBootInputs()
	if err != nil {
		return books.BootInputs{}, err
	}
	set := func(p coreconfig.Path, v string) {
		if in.Flags == nil {
			in.Flags = map[coreconfig.Path]string{}
		}
		in.Flags[p] = v
	}
	for name, path := range serveFlagConfigPaths {
		if f := fs.Lookup(name); f != nil && f.Changed {
			set(path, f.Value.String())
		}
	}
	host, port := fs.Lookup("host"), fs.Lookup("port")
	if host != nil && port != nil && (host.Changed || port.Changed) {
		set("core.http.listen", net.JoinHostPort(host.Value.String(), port.Value.String()))
	}
	return in, nil
}

// rootBootInputs is bootInputs over the root command's persistent flags, as
// serve and the offline commands read them once they are parsed.
func rootBootInputs() (books.BootInputs, error) {
	return bootInputs(rootCmd.PersistentFlags())
}

// resolveCLIPaths is the store path and the state directory the root flags and
// the environment name, resolved the way serve resolves them (books.ResolveBootPaths),
// for the CLI paths that open the store or read the swap slot themselves.
func resolveCLIPaths() (dbPath, stateDir string, err error) {
	boot, err := rootBootInputs()
	if err != nil {
		return "", "", err
	}
	return books.ResolveBootPaths(context.Background(), books.Module(), boot)
}

// resolveChildPaths is resolveCLIPaths for the op-runner child, a fresh process
// the parent handed the store it resolved (childDB, registry.EnvChildDBPath):
// that path is the child's --db, the highest operator layer, so a parent that
// took its path from a flag or the unit's environment is followed exactly, and
// a child started with neither still opens the parent's store. "" names no
// path and leaves the layers alone.
func resolveChildPaths(childDB string) (dbPath, stateDir string, err error) {
	boot, err := rootBootInputs()
	if err != nil {
		return "", "", err
	}
	if childDB != "" {
		abs, err := filepath.Abs(childDB)
		if err != nil {
			return "", "", fmt.Errorf("the parent's store path %q: %w", childDB, err)
		}
		if boot.Flags == nil {
			boot.Flags = map[coreconfig.Path]string{}
		}
		boot.Flags["core.storage.path"] = abs
	}
	return books.ResolveBootPaths(context.Background(), books.Module(), boot)
}
