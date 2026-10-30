// file: internal/walkresume/flag.go
// version: 1.0.0
// guid: 655504b5-988d-422c-a2e2-f1c054e1262b
// last-edited: 2026-10-30

package walkresume

import "sync/atomic"

// flag is a one-way atomic boolean: workers read it per item, the serialized
// checkpoint callback sets it.
type flag struct{ v atomic.Bool }

func newFlag() *flag { return &flag{} }

func (f *flag) set()        { f.v.Store(true) }
func (f *flag) isSet() bool { return f.v.Load() }
