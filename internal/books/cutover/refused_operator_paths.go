// file: internal/books/cutover/refused_operator_paths.go
// version: 1.0.0
// guid: 5b8e2d94-7c31-4f0a-a6e5-1d9c0b3f7a28
// last-edited: 2026-11-28

package cutover

import (
	"regexp"
	"strings"

	"github.com/falkcorp/mediacore/config"
)

// RefusedOperatorPath is an operator-layer setting (an environment variable or a
// key of the operator file) that no longer reaches anything, and that a host may
// still be setting. Core drops an unknown integrations path (an env name it
// cannot place, or a file key that no section owns) with at most a warning, so a
// value an operator set would vanish and the service would run on a different
// setting than the host file says. A host that sets one is refused instead, by
// name, before boot (the books boot check) and before install (the deploy
// pre-flight reads EnvPattern from deploy/retired-env-names.txt).
//
// The refusal never copies or rewrites the value, and never prints it: it names
// the key, the setting to use instead, and nothing the host supplied.
type RefusedOperatorPath struct {
	// Path is the dotted operator path. A <placeholder> segment stands for any
	// one non-empty segment.
	Path string
	// Instead says where to set it now: a path, or a short phrase. It reads after
	// "set" in the deploy check's message ("set <Instead> instead").
	Instead string
	// Message is the whole sentence the boot refusal ends with, naming the setting
	// to use. It holds no value.
	Message string
}

// RefusedOperatorPaths is the list. It is not part of Table(): none of these is a
// target of a HEAD setting, so the passthrough predicate, the canonical env
// names and the M6 overlay never see them.
//
//   - integrations.ai.openai.base_url was HEAD's openai_base_url spelling. The
//     adapter's field is url, and core keys an operator value by the path's last
//     segment, so a value left at base_url would be ignored and requests, with
//     the API key, would go to the stored URL or the default endpoint.
//   - integrations.metadata.<source id>.credentials.<k> was a passthrough secret
//     family. M2 moves each source's credentials into the secrets store under the
//     same name, and M6 binds them to the instance, so the operator layer is no
//     longer a way to supply one: a credential is a stored secret.
func RefusedOperatorPaths() []RefusedOperatorPath {
	return []RefusedOperatorPath{
		{
			Path:    "integrations.ai.openai.base_url",
			Instead: "integrations.ai.openai.url",
			Message: "it was renamed integrations.ai.openai.url (the OpenAI adapter's field, and OPENAI_BASE_URL binds to it): set that and remove this one",
		},
		{
			Path:    "integrations.metadata.<source id>.credentials.<k>",
			Instead: "the credential as a stored secret (Settings, Metadata tab, the source's API key; PUT /api/v1/config), not in the environment or the operator file",
			Message: "a metadata source's credential is a stored secret and is no longer read from the environment or the operator file: set it in Settings, Metadata tab (the source's API key field, or PUT /api/v1/config) and remove this setting",
		},
	}
}

// segments splits Path.
func (r RefusedOperatorPath) segments() []string { return strings.Split(r.Path, ".") }

// MatchesPath reports whether the dotted operator path p is this one, a
// placeholder segment matching any one non-empty segment.
func (r RefusedOperatorPath) MatchesPath(p string) bool {
	pat, segs := r.segments(), strings.Split(p, ".")
	if len(pat) != len(segs) {
		return false
	}
	for i, want := range pat {
		switch {
		case segs[i] == "":
			return false
		case strings.HasPrefix(want, "<") && strings.HasSuffix(want, ">"):
		case want != segs[i]:
			return false
		}
	}
	return true
}

// EnvPattern is the canonical env name of Path, each placeholder as `*`
// (CanonicalEnv's spelling otherwise): ABO_INTEGRATIONS_AI_OPENAI_BASE_URL, and
// ABO_INTEGRATIONS_METADATA_*_CREDENTIALS_* for the family. A `*` stands for one
// or more characters.
func (r RefusedOperatorPath) EnvPattern() string {
	segs := r.segments()
	for i, s := range segs {
		if strings.HasPrefix(s, "<") && strings.HasSuffix(s, ">") {
			segs[i] = "*"
		}
	}
	return CanonicalEnv(config.Path(strings.Join(segs, ".")))
}

// MatchesEnv reports whether the env name is this one's canonical spelling. The
// name is matched exactly and in capitals, as core's env resolver reads it.
func (r RefusedOperatorPath) MatchesEnv(name string) bool {
	return envGlob(r.EnvPattern()).MatchString(name)
}

// envGlob compiles an EnvPattern: `*` is one or more characters, anything else
// is literal.
func envGlob(pattern string) *regexp.Regexp {
	parts := strings.Split(pattern, "*")
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}
	return regexp.MustCompile("^" + strings.Join(parts, ".+") + "$")
}
