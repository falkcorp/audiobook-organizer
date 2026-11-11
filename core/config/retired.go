// file: core/config/retired.go
// version: 1.0.2
// guid: 4a1fd550-2ff7-4d1a-bd04-56e8285dd1f9
// last-edited: 2026-11-11

package config

import (
	"fmt"
	"slices"
	"strings"
)

// Retired names (05-config §1.3.6): the env names and top-level file keys an
// app read before core, handed in through Options.Retired. A retired name is
// never read. When one is set, config decides from the key it became: a
// ProtectAdmin+ key, a secret key, a passthrough path the app marks Refuse,
// a file key whose subtree holds one, or a key of a core section or of an
// enabled module that the app's list names but no section declares refuses
// boot with *RetiredNameError naming every such use; anything else is
// ignored with one warning per boot (a module key of a module that registers
// no section is reported Unchecked),
// health config_retired_name_ignored and the gauge
// mediacore_config_retired_names_set. Core keys are judged after Boot reads
// the operator layers, module and passthrough keys after Load, both before
// storage opens (§1.4 steps 2-3).

// healthRetiredIgnored is raised on core.config while a warn-class retired
// name is set (05-health).
const healthRetiredIgnored = "config_retired_name_ignored"

// retiredKey identifies one retired name: an env name and a file key may
// share a spelling.
type retiredKey struct {
	name string
	file bool
}

// checkRetiredNames validates Options.Retired at NewStore: a name is set, a
// file key is not one of the canonical top-level keys, and no name is listed
// twice.
func checkRetiredNames(rs []RetiredName) error {
	seen := map[retiredKey]bool{}
	for _, r := range rs {
		k := retiredKey{r.Name, r.File}
		switch {
		case r.Name == "":
			return fmt.Errorf("config: Options.Retired holds an entry with no name (canonical %q)", r.Canonical)
		case r.File && (r.Name == fileCore || r.Name == fileModules || r.Name == integrationsRoot):
			return fmt.Errorf("config: Options.Retired lists the canonical top-level file key %q", r.Name)
		case seen[k]:
			return fmt.Errorf("%w: Options.Retired lists %q twice", ErrDuplicate, r.Name)
		}
		seen[k] = true
	}
	return nil
}

// retiredNames splits Options.Retired into the env names and the file keys.
func retiredNames(rs []RetiredName) (env, file map[string]bool) {
	env, file = map[string]bool{}, map[string]bool{}
	for _, r := range rs {
		if r.File {
			file[r.Name] = true
		} else {
			env[r.Name] = true
		}
	}
	return env, file
}

// topKeyer is a file layer that can list its top-level keys.
type topKeyer interface{ topKeys() ([]string, error) }

// retiredIsSet reports whether r is set: an env name with a value that is
// not unset (empty or null, §1.3.5), or a file key the operator file holds.
func (s *Store) retiredIsSet(r RetiredName, fileKeys []string) bool {
	if r.File {
		return slices.Contains(fileKeys, r.Name)
	}
	return !envUnset(s.envVars[r.Name])
}

// coreScoped reports whether p is a core path: one Boot can judge, because
// every core section registers before Boot.
func coreScoped(p Path) bool { return p == fileCore || strings.HasPrefix(string(p), fileCore+pathSep) }

// keysUnder lists the registered keys p names: the key at p, every key under
// p (a file key spanning a subtree), and the key p lies under (a path into a
// key's value). A map section's and an entry list's templates count.
func (s *Store) keysUnder(p Path) []*keyInfo {
	var out []*keyInfo
	match := func(rel, want string) bool {
		return want == "" || rel == want || strings.HasPrefix(rel, want+pathSep) || strings.HasPrefix(want, rel+pathSep)
	}
	for _, sec := range s.sections {
		switch {
		case under(sec.path, p):
			out = append(out, sec.keys...)
		case under(p, sec.path):
			rest := strings.TrimPrefix(strings.TrimPrefix(string(p), string(sec.path)), pathSep)
			if sec.isMap {
				_, rest, _ = strings.Cut(rest, pathSep)
			}
			for _, k := range sec.keys {
				if match(k.relName(), rest) {
					out = append(out, k)
				}
			}
		}
		for _, lk := range sec.entryLists {
			parent := lk.entryParent()
			switch {
			case under(parent, p):
				out = append(out, lk.entrySecrets...)
			case under(p, parent) && p != parent:
				rest := strings.TrimPrefix(string(p), string(parent)+pathSep)
				_, rel, _ := strings.Cut(rest, pathSep)
				for _, d := range lk.entrySecrets {
					if rel == "" || rel == d.rel[0] {
						out = append(out, d)
					}
				}
			}
		}
	}
	return out
}

// retiredVerdict is how one set retired name is judged.
type retiredVerdict int

const (
	retiredLater     retiredVerdict = iota // its key cannot be judged yet (a module or passthrough key at Boot)
	retiredWarn                            // ignored with a warning
	retiredRefuse                          // boot is refused
	retiredUnchecked                       // a module key of a module that registers no sections: ignored with a warning, its class unknown
)

// moduleHasSections reports whether any section under modules.<id> is
// registered: the kernel registers an enabled module's sections only.
func (s *Store) moduleHasSections(id string) bool {
	root := Path(fileModules + pathSep + id)
	for _, sec := range s.sections {
		if under(sec.path, root) {
			return true
		}
	}
	return false
}

// retiredJudge decides one set retired name (05-config §1.3.6). final is
// false at Boot, when only core keys can be judged.
func (s *Store) retiredJudge(r RetiredName, final bool) retiredVerdict {
	verdict := func(refuse bool) retiredVerdict {
		if refuse {
			return retiredRefuse
		}
		return retiredWarn
	}
	c := r.Canonical
	switch {
	case c == "":
		return retiredWarn
	case s.passthrough(c):
		if !final {
			return retiredLater
		}
		return verdict(r.Refuse)
	case !coreScoped(c) && !final:
		return retiredLater
	case coreScoped(c) || strings.HasPrefix(string(c), fileModules+pathSep):
		ks := s.keysUnder(c)
		if len(ks) == 0 {
			// Every core section registers before Boot, and an enabled
			// module registers all of its sections, so a missing key of
			// either is a defect of the app's list: refused, because its
			// class is unknown. A module that registers no section is not
			// enabled at this boot (or, in config check, its sections are
			// not handed in): nothing reads the key and its class is
			// unknown, so the use is reported as unchecked.
			if coreScoped(c) {
				return retiredRefuse
			}
			id, _, _ := strings.Cut(strings.TrimPrefix(string(c), fileModules+pathSep), pathSep)
			if s.moduleHasSections(id) {
				return retiredRefuse
			}
			return retiredUnchecked
		}
		for _, k := range ks {
			if k.protect >= ProtectAdmin || k.secret {
				return retiredRefuse
			}
		}
		return retiredWarn
	}
	// Neither a core, a module nor a passthrough path (an integrations path
	// with no Passthrough function, as in config check): the app's own
	// statement of its class decides.
	return verdict(r.Refuse)
}

// checkRetired is the retired-name rule at Boot (final false: core keys
// only) and at Load (final true: every key). A Boot that refuses judges every
// key as it stands, so RetiredSet and the error list every use, not only
// the core ones (module keys then read as Unchecked: no module section is
// registered yet). It records RetiredSet, warns once per name per boot,
// raises config_retired_name_ignored and sets the gauge; it returns
// *RetiredNameError naming every refused use. It runs before the boot record
// or any store is opened.
func (s *Store) checkRetired(final bool) error {
	var fileKeys []string
	if tk, ok := s.file.(topKeyer); ok {
		var err error
		if fileKeys, err = tk.topKeys(); err != nil {
			return err
		}
	}
	uses, refused := s.judgeRetired(fileKeys, final)
	if !final && len(refused) > 0 {
		uses, refused = s.judgeRetired(fileKeys, true)
	}
	warned := 0
	for _, u := range uses {
		if u.Refused {
			continue
		}
		warned++
		k := retiredKey{u.Name, u.File}
		if s.retiredLogged[k] {
			continue
		}
		s.retiredLogged[k] = true
		args := []any{"name", u.Name, "file_key", u.File}
		if u.Canonical != "" {
			args = append(args, "canonical", u.Canonical, "canonical_env", u.CanonicalEnv)
		} else {
			args = append(args, "dropped", true)
		}
		if msg := s.retiredMessage(k); msg != "" {
			args = append(args, "message", msg)
		}
		if u.Unchecked {
			args = append(args, "unchecked", "its module registers no settings at this boot")
		}
		s.log.Warn("config: a retired config name is set; it is never read and its value is ignored", args...)
	}
	s.retired = uses
	s.met.retired.Store(int64(warned))
	if warned > 0 {
		s.health[healthRetiredIgnored] = fmt.Sprintf("%d retired config names set and ignored", warned)
	} else {
		delete(s.health, healthRetiredIgnored)
	}
	if len(refused) > 0 {
		return &RetiredNameError{Uses: refused}
	}
	return nil
}

// judgeRetired judges every set retired name; see checkRetired.
func (s *Store) judgeRetired(fileKeys []string, final bool) (uses, refused []RetiredUse) {
	for _, r := range s.o.Retired {
		if !s.retiredIsSet(r, fileKeys) {
			continue
		}
		v := s.retiredJudge(r, final)
		if v == retiredLater {
			continue
		}
		u := RetiredUse{Name: r.Name, File: r.File, Canonical: r.Canonical, Refused: v == retiredRefuse, Unchecked: v == retiredUnchecked}
		if r.Canonical != "" {
			u.CanonicalEnv = envName(s.prefix, r.Canonical)
		}
		uses = append(uses, u)
		if u.Refused {
			refused = append(refused, u)
		}
	}
	return uses, refused
}

// retiredMessage is the app's reason for the retired name k, "" when none.
func (s *Store) retiredMessage(k retiredKey) string {
	for _, r := range s.o.Retired {
		if r.Name == k.name && r.File == k.file {
			return r.Message
		}
	}
	return ""
}
