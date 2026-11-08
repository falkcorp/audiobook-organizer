// file: core/storage/restorejournal/restorejournaltest/probe.go
// version: 1.0.0
// guid: 9738fa4d-d83f-4f29-bc1f-2f63232106ca
// last-edited: 2026-11-08

// Package restorejournaltest is restorejournal's test kit: a Probe that
// models a second filesystem on a test host's one. Test support only.
package restorejournaltest

import "github.com/falkcorp/mediacore/storage/restorejournal"

// OtherDevice is Probe, except that every existing path in one of Dirs
// reports a device of its own. A test host has one filesystem, and a
// reset's delete entry on the DB's filesystem is refused, at Stage and at
// P0a's step a (05-storage §5S.3.15). So a test stages its delete entries
// with this probe, and boots with it as bootseam.Seams.Probe, to model them
// on a second filesystem.
type OtherDevice struct {
	restorejournal.Probe
	Dirs []string
}

func (p OtherDevice) shift(path string, dev uint64, err error) (uint64, error) {
	for _, d := range p.Dirs {
		if err == nil && restorejournal.InTree(path, d) {
			return dev + 1, nil
		}
	}
	return dev, err
}

// ItemDev is Probe's, shifted for a path in Dirs.
func (p OtherDevice) ItemDev(path string) (uint64, error) {
	dev, err := p.Probe.ItemDev(path)
	return p.shift(path, dev, err)
}

// DirDev is Probe's, shifted for a path in Dirs.
func (p OtherDevice) DirDev(path string) (uint64, error) {
	dev, err := p.Probe.DirDev(path)
	return p.shift(path, dev, err)
}
