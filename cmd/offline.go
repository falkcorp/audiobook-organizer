// file: cmd/offline.go
// version: 1.0.0
// guid: deab4331-88da-46d1-b820-c48adf25a336
// last-edited: 2026-11-08

package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/falkcorp/audiobook-organizer/internal/books"
	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/module"
)

// offlineOptions is the app.Options core's offline commands run with,
// resolved when one runs, after its flags are parsed and the config loaded.
// A var so a test can point it at a temporary store.
var offlineOptions = books.OfflineOptions

// addOfflineCommands wraps each entry of app.OfflineCommands as a cobra
// command under root (06 §6.4.5; MOD2-4, R9-9, COR9-6): core's offline
// commands are reachable from ABO's CLI without ABO importing kernel. An
// entry's space-separated Name is its path; a parent ("storage") is made
// once and holds no Run of its own. Cobra parses none of the command's
// flags, so Run parses its own and its help is core's; the root's
// persistent flags (--db, --config, --db-type and the rest) are taken out
// of the arguments and parsed first, then the config is loaded with them
// (the root's PersistentPreRun skips these commands, which would load it
// before the flags were set), so an offline command names the same store
// serve would. The list is read twice: here, for the names and the help,
// with ABO's identity only; and when a command runs, with the store path
// and state directory config resolves.
func addOfflineCommands(root *cobra.Command, mods func() []module.Module) {
	parents := map[string]*cobra.Command{}
	for _, c := range app.OfflineCommands(app.Options{AppID: books.AppID}, mods()...) {
		path := strings.Fields(c.Name)
		if len(path) == 0 {
			continue
		}
		parent := root
		for i, p := range path[:len(path)-1] {
			key := strings.Join(path[:i+1], " ")
			next, ok := parents[key]
			if !ok {
				next = &cobra.Command{Use: p, Short: "Offline " + key + " commands"}
				parent.AddCommand(next)
				parents[key] = next
			}
			parent = next
		}
		name := c.Name
		parent.AddCommand(&cobra.Command{
			Use:                path[len(path)-1],
			Short:              c.Short,
			Long:               c.Long,
			DisableFlagParsing: true,
			SilenceUsage:       true,
			Annotations:        map[string]string{skipAppInitAnnotation: "true"},
			RunE: func(cmd *cobra.Command, args []string) error {
				persistent := cmd.Root().PersistentFlags()
				own, inherited := splitFlags(persistent, args)
				if err := persistent.Parse(inherited); err != nil {
					return err
				}
				// Help needs no store: it is printed before config would
				// resolve (and perhaps create) the state directory.
				opts := app.Options{AppID: books.AppID}
				if !wantsHelp(own) {
					initConfig()
					var err error
					if opts, err = offlineOptions(); err != nil {
						return err
					}
				}
				for _, oc := range app.OfflineCommands(opts, mods()...) {
					if oc.Name == name {
						return oc.Run(cmd.Context(), own, cmd.OutOrStdout())
					}
				}
				return fmt.Errorf("%s: not an offline command of this build", name)
			},
		})
	}
}

// splitFlags separates args into the flags fs defines, each with its value
// (--name=v, --name v, -n v, or a bool alone), and everything else, in
// order. Everything after "--" is the command's.
func splitFlags(fs *pflag.FlagSet, args []string) (own, inherited []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return append(own, args[i:]...), inherited
		}
		var f *pflag.Flag
		hasValue := false
		switch {
		case strings.HasPrefix(a, "--"):
			var name string
			name, _, hasValue = strings.Cut(a[2:], "=")
			f = fs.Lookup(name)
		case strings.HasPrefix(a, "-") && len(a) >= 2:
			f = fs.ShorthandLookup(a[1:2])
			hasValue = len(a) > 2
		}
		if f == nil {
			own = append(own, a)
			continue
		}
		inherited = append(inherited, a)
		if !hasValue && f.NoOptDefVal == "" && i+1 < len(args) {
			i++
			inherited = append(inherited, args[i])
		}
	}
	return own, inherited
}

// wantsHelp reports whether args ask for help before any "--".
func wantsHelp(args []string) bool {
	for _, a := range args {
		switch a {
		case "--":
			return false
		case "-h", "--help", "-help":
			return true
		}
	}
	return false
}
