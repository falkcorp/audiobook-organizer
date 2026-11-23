// file: core/app/updater_options_test.go
// version: 0.1.0
// guid: 3e9a41d7-6b02-4c58-b1f3-a7d05c82e964
// last-edited: 2026-11-22

package app

import (
	"context"
	"io"
	"testing"

	"github.com/falkcorp/mediacore/updater"
)

type noReleases struct{}

func (noReleases) Latest(context.Context, updater.Channel) (updater.Release, error) {
	return updater.Release{}, updater.ErrNoUpdate
}

func (noReleases) Open(context.Context, updater.Asset) (io.ReadCloser, int64, error) {
	return nil, -1, updater.ErrNoUpdate
}

// TestKernelConfig_UpdaterReachesTheKernel: Options.Updater is the kernel's
// Config.Updater, copied so a later change to the app's value cannot move the
// kernel's; nil stays nil. The kernel wires the updater from it only with
// IdentityBoot also set (kernel.updaterDeclared).
func TestKernelConfig_UpdaterReachesTheKernel(t *testing.T) {
	o := updater.Options{AppID: "gotham", BinaryName: "batmobile", Source: noReleases{}, DefaultMode: updater.ModeCheck}
	on, err := kernelConfig(Options{AppID: "gotham", Updater: &o}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if on.Updater == nil || on.Updater.AppID != "gotham" || on.Updater.BinaryName != "batmobile" || on.Updater.Source == nil || on.Updater.DefaultMode != updater.ModeCheck {
		t.Fatalf("kernel.Config.Updater = %+v, want Options.Updater's fields", on.Updater)
	}
	if on.Updater == &o {
		t.Error("kernel.Config.Updater aliases the app's Options.Updater")
	}
	off, err := kernelConfig(Options{AppID: "gotham"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if off.Updater != nil {
		t.Errorf("an app with no updater got kernel.Config.Updater = %+v", off.Updater)
	}
}
