// file: internal/config/persistence.go
// version: 1.62.3
// guid: 9c8d7e6f-5a4b-3c2d-1e0f-9a8b7c6d5e4f
// last-edited: 2026-11-09

package config

import "github.com/spf13/viper"

// RootDirOverride is viper's root_dir (the environment or the config file)
// when set and not empty. books' reset hook, which resolves the library root
// from the stored settings with the service stopped, applies it over them.
func RootDirOverride() (string, bool) {
	if !viper.IsSet("root_dir") {
		return "", false
	}
	val := viper.GetString("root_dir")
	return val, val != ""
}
