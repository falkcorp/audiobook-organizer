// file: internal/books/updater_options.go
// version: 0.1.0
// guid: 6c1e84b2-3d57-4f09-a2b8-71e5c0d94a36
// last-edited: 2026-11-22

package books

import (
	"strings"

	coreupdater "github.com/falkcorp/mediacore/updater"
	"github.com/falkcorp/mediacore/updater/github"
)

// ABO's side of core's updater (05-updater §5U.3): the options core's updater is
// built with, which name the repository, the binary and the release asset so
// core names no app. They lived in internal/updater until F8-04b 8b-4; that
// package cannot import this one (books imports the server, which imports it),
// so its gin path keeps a copy of the three names until the gin half is
// deleted, and TestReleaseNamesMatchTheGinPath holds them equal.
const (
	// releaseOwner and releaseRepo name this app's release repository.
	releaseOwner = "falkcorp"
	releaseRepo  = "audiobook-organizer"
	// releaseBinary is the raw release asset's prefix ("<releaseBinary>_<Os>_<arch>"
	// in a published release) and the user agent's product.
	releaseBinary = "audiobook-organizer"
)

// releaseSource is the release source core's updater is built with: GitHub
// releases of falkcorp/audiobook-organizer. The asset looked for in a release
// is the Options' AssetName (FetchOptions).
func releaseSource(opts ...github.Option) coreupdater.Source {
	return github.New(releaseOwner, releaseRepo, opts...)
}

// CoreOptions is the core updater's Options for this app: the source, the
// binary name and the app ID its self-test holds a downloaded binary to, and
// core's RawBinary default for the asset name ("audiobook-organizer-<goos>-<goarch>"),
// which no release publishes (see ReleasedAsset). FetchOptions, not this, is
// what core's updater is handed.
func CoreOptions() coreupdater.Options {
	return coreupdater.Options{AppID: AppID, BinaryName: releaseBinary, Source: releaseSource(), DefaultMode: coreupdater.ModeCheck}
}

// FetchOptions is the core updater's Options for this app with the asset name
// releases really publish (owner ruling O-5): CoreOptions with ReleasedAsset.
// It is app.Options.Updater, which `updater fetch-release` reads today and the
// F8-04b flip hands the kernel.
func FetchOptions() coreupdater.Options {
	o := CoreOptions()
	o.AssetName = ReleasedAsset
	return o
}

// ReleasedAsset is the raw-binary asset of a published release for goos and
// goarch, as .goreleaser.yml names it: "{{ .ProjectName }}_{{ title .Os }}_"
// then x86_64 for amd64, i386 for 386 and the architecture as it is
// otherwise, such as audiobook-organizer_Linux_x86_64. TestReleasedAssetMatchesGoreleaser
// holds it to the file. (The Arm suffix goreleaser adds for goarm is not
// built: the matrix is linux and darwin on amd64 and arm64.)
func ReleasedAsset(_ coreupdater.Release, goos, goarch string) string {
	arch := goarch
	switch goarch {
	case "amd64":
		arch = "x86_64"
	case "386":
		arch = "i386"
	}
	osName := goos
	if osName != "" {
		osName = strings.ToUpper(osName[:1]) + osName[1:]
	}
	return releaseBinary + "_" + osName + "_" + arch
}
