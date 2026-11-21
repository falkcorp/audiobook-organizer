// file: core/kernel/logconfig_test.go
// version: 0.1.0
// guid: 2f8b6d41-0c97-4e35-a1d8-4b7e9c3f5a60
// last-edited: 2026-11-21

package kernel

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/coretest/logcapture"
	"github.com/falkcorp/mediacore/log"
)

// waitLogCondition polls until cond holds. OnChange delivery is asynchronous
// (a goroutine with a coalescing mailbox), so a live change is observed
// eventually, never synchronously with Apply.
func waitLogCondition(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s: the change did not apply live", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// liveLogKernel is a kernel logging to tee, with a started config store whose
// core.log handle the kernel watches. The store's changes reach the logger.
func liveLogKernel(t *testing.T) (k *Kernel, cb *configBoot, tee *teeBuffer) {
	t.Helper()
	tee = &teeBuffer{}
	k = mustNew(t, Config{AppID: "gotham", LogOutput: tee})
	cb, _ = startedCfg(t, configIn(t))
	stop, err := k.watchLogConfig(cb.handles)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	return k, cb, tee
}

// TestCoreLogConfig_AppliesLive: level, levels, redact_keys and add_source
// are changed through the config store at run time and each takes effect on
// loggers built before the change, without a restart, while other goroutines
// log through the same pipeline (run under -race).
func TestCoreLogConfig_AppliesLive(t *testing.T) {
	k, cb, tee := liveLogKernel(t)
	ctx := context.Background()
	admin := interactiveAdmin()
	scanner := k.Logging().Scope("books", "scanner") // exists before every change
	redactorHeldByMetricsAndHTTP := k.redactor      // the pointer boot hands to them

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			l := k.Logging().Scope("core", "noise")
			for {
				select {
				case <-stop:
					return
				default:
				}
				l.Info("alfred polished the batmobile", "batcave_door_code", "1939")
				_ = redactorHeldByMetricsAndHTTP.String("batcave_door_code", "1939")
			}
		})
	}
	defer func() { close(stop); wg.Wait() }()

	t.Run("level", func(t *testing.T) {
		if k.Logging().Level("books.scanner") != log.LevelInfo || scanner.Enabled(ctx, log.LevelDebug) {
			t.Fatal("debug is already enabled before the change")
		}
		if err := applyOne(cb, admin, "core.log.level", `"debug"`); err != nil {
			t.Fatal(err)
		}
		waitLogCondition(t, "core.log.level=debug", func() bool { return scanner.Enabled(ctx, log.LevelDebug) })
	})

	t.Run("levels", func(t *testing.T) {
		if err := applyOne(cb, admin, "core.log.levels", `{"books.scanner":"error"}`); err != nil {
			t.Fatal(err)
		}
		waitLogCondition(t, "core.log.levels[books.scanner]=error", func() bool { return !scanner.Enabled(ctx, log.LevelWarn) })
		if !k.Logging().Scope("core", "other").Enabled(ctx, log.LevelDebug) {
			t.Error("the per-package entry changed a scope it does not name")
		}
	})

	t.Run("redact_keys", func(t *testing.T) {
		const key = "batcave_door_code"
		if redactorHeldByMetricsAndHTTP.String(key, "1939") != "1939" {
			t.Fatal("the key is already redacted before the change")
		}
		if err := applyOne(cb, admin, "core.log.redact_keys", `["`+key+`"]`); err != nil {
			t.Fatal(err)
		}
		waitLogCondition(t, "core.log.redact_keys", func() bool {
			return redactorHeldByMetricsAndHTTP.String(key, "1939") != "1939"
		})
		// The log line, not only the Redactor, and a record logged after the change.
		k.Logging().Scope("core", "probe").Info("door opened", key, "7777")
		if strings.Contains(tee.String(), "7777") {
			t.Errorf("a record logged after the change printed the key's value: %s", tee.String())
		}
		// Removing the key from the setting stops redacting it.
		if err := applyOne(cb, admin, "core.log.redact_keys", `[]`); err != nil {
			t.Fatal(err)
		}
		waitLogCondition(t, "core.log.redact_keys=[]", func() bool {
			return redactorHeldByMetricsAndHTTP.String(key, "1939") == "1939"
		})
	})

	t.Run("add_source", func(t *testing.T) {
		probe := func() string {
			k.Logging().Scope("core", "probe").Warn("source probe")
			return tee.String()
		}
		if strings.Contains(probe(), " source=") {
			t.Fatal("source printed before add_source was turned on")
		}
		if err := applyOne(cb, admin, "core.log.add_source", `true`); err != nil {
			t.Fatal(err)
		}
		waitLogCondition(t, "core.log.add_source=true", func() bool { return strings.Contains(probe(), " source=") })
	})
}

// TestCoreLogConfig_ChangeDoesNotResetOtherKeys: the pipeline was built from
// Config.Log (ABO's log_level), which the config store does not hold, so a
// change to one key must not put the others back to the store's values.
func TestCoreLogConfig_ChangeDoesNotResetOtherKeys(t *testing.T) {
	boot := log.DefaultSettings()
	boot.Level = "debug" // the store's core.log.level is still "info"
	boot.Levels = map[string]string{"books": "warn"}
	tee := &teeBuffer{}
	k := mustNew(t, Config{AppID: "gotham", LogOutput: tee, Log: &boot})
	cb, _ := startedCfg(t, configIn(t))
	stop, err := k.watchLogConfig(cb.handles)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	books := k.Logging().Module("books")
	if err := applyOne(cb, interactiveAdmin(), "core.log.redact_keys", `["batcave_door_code"]`); err != nil {
		t.Fatal(err)
	}
	waitLogCondition(t, "redact_keys", func() bool { return k.redactor.String("batcave_door_code", "1939") != "1939" })
	if !k.Logging().Scope("core", "x").Enabled(context.Background(), log.LevelDebug) {
		t.Error("changing redact_keys reset the root level to the store's default")
	}
	if books.Enabled(context.Background(), log.LevelInfo) {
		t.Error("changing redact_keys dropped the per-package level Config.Log set")
	}
}

// TestCoreLogConfig_ConfigRefusesABadValue: an invalid value never reaches the
// subscriber, because config refuses it when it validates the section, so the
// logger keeps its state and Apply says why.
func TestCoreLogConfig_ConfigRefusesABadValue(t *testing.T) {
	k, cb, _ := liveLogKernel(t)
	admin := interactiveAdmin()
	scanner := k.Logging().Scope("books", "scanner")
	if err := applyOne(cb, admin, "core.log.levels", `{"books":"debug"}`); err != nil {
		t.Fatal(err)
	}
	if err := applyOne(cb, admin, "core.log.redact_keys", `["batcave_door_code"]`); err != nil {
		t.Fatal(err)
	}
	waitLogCondition(t, "the good values", func() bool {
		return scanner.Enabled(context.Background(), log.LevelDebug) && k.redactor.String("batcave_door_code", "1939") != "1939"
	})

	for key, bad := range map[config.Path]string{
		"core.log.levels":      `{"books":"loud"}`,
		"core.log.redact_keys": `["api_key","-_."]`,
	} {
		if err := applyOne(cb, admin, key, bad); err == nil {
			t.Errorf("%s=%s was accepted", key, bad)
		}
	}
	time.Sleep(50 * time.Millisecond) // a wrongly published change would have been delivered by now
	if !scanner.Enabled(context.Background(), log.LevelDebug) {
		t.Error("a refused change altered the levels")
	}
	if k.redactor.String("batcave_door_code", "1939") == "1939" {
		t.Error("a refused redact_keys change altered the Redactor")
	}
}

// TestApplyLogSettings_RefusedKeepsState: if a bad value did reach the
// subscriber (config's Validator missed it), nothing is half-applied and the
// refusal is reported.
func TestApplyLogSettings_RefusedKeepsState(t *testing.T) {
	good := log.DefaultSettings()
	good.Level = "warn"
	good.RedactKeys = []string{"batcave_door_code"}
	tee := &teeBuffer{}
	k := mustNew(t, Config{AppID: "gotham", LogOutput: tee, Log: &good})
	l := k.coreLog("logconfig")

	bad := log.DefaultSettings()
	bad.Level = "debug"
	bad.RedactKeys = nil
	bad.Levels = map[string]string{"books": "loud"}
	if err := k.applyLogSettings(l, bad); err == nil {
		t.Fatal("a bad levels value was applied")
	}
	if got := k.Logging().Level("core"); got != log.LevelWarn {
		t.Errorf("the root level became %v although the change was refused", got)
	}
	if k.redactor.String("batcave_door_code", "1939") == "1939" {
		t.Error("redaction was dropped although the change was refused")
	}
	if !strings.Contains(tee.String(), "core.log change refused") {
		t.Errorf("the refusal was not reported: %q", tee.String())
	}
}

// TestApplyLogSettings_AdoptedLoggerStillRedacts: with Config.Logger there is
// no pipeline to re-level, but redact_keys still reaches the shared Redactor.
func TestApplyLogSettings_AdoptedLoggerStillRedacts(t *testing.T) {
	k := mustNew(t, Config{AppID: "gotham", Logger: logcapture.New(t).Logger()})
	if k.logging != nil {
		t.Fatal("an adopted logger built a pipeline")
	}
	s := log.DefaultSettings()
	s.RedactKeys = []string{"batcave_door_code"}
	if err := k.applyLogSettings(k.coreLog("logconfig"), s); err != nil {
		t.Fatal(err)
	}
	if k.redactor.String("batcave_door_code", "1939") == "1939" {
		t.Error("redact_keys did not reach the Redactor under an adopted logger")
	}
}

// TestWatchLogConfig_NeedsTheHandle: handles without core.log are an error,
// not a silent boot-only setting.
func TestWatchLogConfig_NeedsTheHandle(t *testing.T) {
	k := mustNew(t, Config{AppID: "gotham", LogOutput: &teeBuffer{}})
	if _, err := k.watchLogConfig(nil); err == nil || !strings.Contains(err.Error(), "core.log") {
		t.Errorf("watchLogConfig(nil) = %v, want an error naming core.log", err)
	}
	cb := bootCfg(t, configIn(t))
	if !slices.ContainsFunc(cb.handles, func(h any) bool { _, ok := h.(*config.Handle[log.Settings]); return ok }) {
		t.Fatal("core.log is not among the core handles")
	}
	if _, err := k.watchLogConfig(cb.handles); err != nil {
		t.Errorf("watchLogConfig over the core handles: %v", err)
	}
}
