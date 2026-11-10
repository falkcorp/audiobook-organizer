// file: core/config/flagset.go
// version: 1.0.0
// guid: f5cb2a90-98dc-4823-92ae-1aa47a9bd841
// last-edited: 2026-11-10

package config

import "flag"

// FromFlagSet converts a stdlib FlagSet to Options.Flags using Visit, so only
// the flags the operator explicitly set are read: a flag's default never
// locks a key (05-config §1.3.3). bind maps a flag name to the key it sets;
// a set flag bind does not name is not config's. Apps on cobra/pflag write
// the same few lines over pflag.FlagSet.Visit; core does not import pflag.
func FromFlagSet(fs *flag.FlagSet, bind map[string]Path) map[Path]string {
	out := map[Path]string{}
	if fs == nil {
		return out
	}
	fs.Visit(func(f *flag.Flag) {
		if p, ok := bind[f.Name]; ok {
			out[p] = f.Value.String()
		}
	})
	return out
}
