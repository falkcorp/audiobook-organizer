// file: pkg/ops/cron_fields_test.go
// version: 1.0.0
// guid: 3e8c1b57-94a2-4d06-b7f3-5a1d92c60e84
// last-edited: 2026-10-17

package ops

import "testing"

func TestParseCronFields_GrammarIsOneSource(t *testing.T) {
	for _, bad := range []string{"+5 * * * *", "*/+5 * * * *", "1-+5 * * * *", "-1 * * * *", "0 0 * * MON", "@daily", "*/0 * * * *"} {
		if _, err := ParseCronFields(bad); err == nil {
			t.Errorf("ParseCronFields(%q) accepted", bad)
		}
		// The definition-time validator is the same parser.
		if err := validateCron(bad); err == nil {
			t.Errorf("validateCron(%q) accepted", bad)
		}
	}
	f, err := ParseCronFields("*/15 9-17/2 1,15 * 7")
	if err != nil {
		t.Fatal(err)
	}
	if f.DayOfWeek != 1 { // 7 folds onto Sunday (bit 0)
		t.Errorf("day-of-week = %b, want Sunday only", f.DayOfWeek)
	}
	if !f.DayOfMonthStar && f.DayOfWeekStar {
		t.Error("star flags wrong")
	}
}

func TestIsWriter(t *testing.T) {
	if ReadOnly(ResBooks).IsWriter() {
		t.Error("ReadOnly is a writer")
	}
	if !Writes().IsWriter() || !Writes(ResBooks).IsWriter() {
		t.Error("Writes(...) is not a writer, even when empty")
	}
}
