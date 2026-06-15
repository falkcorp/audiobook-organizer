// file: internal/tools/versions.go
// version: 1.1.0
// guid: f6a7b8c9-d0e1-2345-fabc-345678901234
// last-edited: 2026-06-15

package tools

// ToolRelease describes a pinned binary release for a managed tool.
type ToolRelease struct {
	Version string
	// URLTemplate has {VERSION} and {ARCH} replaced at runtime.
	// ARCH is "amd64" or "arm64" derived from runtime.GOARCH.
	URLTemplate string
	// SHA256 maps "linux/amd64" etc. to the expected hex checksum.
	SHA256 map[string]string
}

// KnownTools maps tool-name → version-string → ToolRelease.
// Old entries stay here so existing managed installs can always be
// checksum-verified, even after a newer version becomes the default.
// To add a version: append a new inner entry and update LatestVersions.
var KnownTools = map[string]map[string]ToolRelease{
	"ollama": {
		"0.30.8": {
			Version:     "0.30.8",
			URLTemplate: "https://github.com/ollama/ollama/releases/download/v{VERSION}/ollama-linux-{ARCH}.tar.zst",
			SHA256: map[string]string{
				"linux/amd64": "ffe2b2c2f2f5f5b30c081ec353c2e0bb2d9ead516064a8e22663b24b8fd8dca0",
			},
		},
	},
	"fpcalc": {
		"1.5.1": {
			Version:     "1.5.1",
			URLTemplate: "https://github.com/acoustid/chromaprint/releases/download/v{VERSION}/chromaprint-fpcalc-{VERSION}-linux-x86_64.tar.gz",
			SHA256: map[string]string{
				"linux/amd64": "4d7433a7f778e5946d7225230681cbcd634e153316ecac87c538c33ac32387a5",
			},
		},
	},
}

// LatestVersions specifies the recommended version for new managed installs.
// Bump this when upgrading a tool; the old version entry in KnownTools stays.
var LatestVersions = map[string]string{
	"ollama": "0.30.8",
	"fpcalc": "1.5.1",
}

// LatestRelease returns the current recommended ToolRelease for name, or
// (ToolRelease{}, false) if the tool is unknown.
func LatestRelease(name string) (ToolRelease, bool) {
	ver, ok := LatestVersions[name]
	if !ok {
		return ToolRelease{}, false
	}
	rel, ok := KnownTools[name][ver]
	return rel, ok
}
