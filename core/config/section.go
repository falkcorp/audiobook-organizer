// file: core/config/section.go
// version: 1.0.0
// guid: 73e52070-753a-40ad-ac2c-01d82403f2d5
// last-edited: 2026-11-10

package config

// Section is a declared, unbound section. Define never fails; Register
// (F3-01) validates. A section path may extend another section's path
// (core.log and core.log.journal) only at a segment the parent's type does not
// declare as a field; an overlap fails Register with ErrDuplicate.
type Section[T any] struct {
	path     Path
	defaults T
	opts     []SectionOption
}

// Define records a section's path, defaults and options. It never fails and
// never validates: an invalid path or option surfaces at Register.
func Define[T any](path Path, defaults T, opts ...SectionOption) Section[T] {
	return Section[T]{path: path, defaults: defaults, opts: append([]SectionOption(nil), opts...)}
}

// Path returns the section's declared path (name coined by F2-09, 05-config
// §1.3.1 has no accessor).
func (s Section[T]) Path() Path { return s.path }

// spec applies the recorded options to a fresh sectionSpec, so no caller can
// alias another's. It is the one reader of a Section: Register (F3-01) uses it
// through an interface with this unexported method.
func (s Section[T]) spec() *sectionSpec {
	sp := &sectionSpec{path: s.path, defaults: s.defaults}
	for _, o := range s.opts {
		if o != nil {
			o(sp)
		}
	}
	return sp
}
