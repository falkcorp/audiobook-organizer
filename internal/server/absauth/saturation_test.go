// file: internal/server/absauth/saturation_test.go
// version: 1.0.0
// guid: 4b91b557-13ea-4dba-b0b6-03391d4ac1bd
// last-edited: 2026-10-21

package absauth

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestSaturationPolicy(t *testing.T) {
	tests := []struct {
		failures int
		delay    time.Duration
		refuse   bool
	}{
		{0, SaturationDelayBase, false},
		{1, SaturationDelayBase + SaturationDelayStep, false},
		{SoftThreshold - 1, min(SaturationDelayBase+time.Duration(SoftThreshold-1)*SaturationDelayStep, SaturationDelayMax), false},
		{SoftThreshold, 0, true},
		{SoftThreshold + 10, 0, true},
	}
	for _, tt := range tests {
		d, refuse := SaturationPolicy(tt.failures)
		if d != tt.delay || refuse != tt.refuse {
			t.Errorf("SaturationPolicy(%d) = %v, %v; want %v, %v", tt.failures, d, refuse, tt.delay, tt.refuse)
		}
		if d > SaturationDelayMax {
			t.Errorf("delay %v exceeds the cap", d)
		}
	}
}

func TestLoginSaturatedMark(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	if LoginSaturated(c) {
		t.Fatal("unmarked request reads as saturated")
	}
	MarkLoginSaturated(c)
	if !LoginSaturated(c) {
		t.Fatal("marked request does not read as saturated")
	}
}

func TestThrottle_AccountFailuresReadsWithoutCharging(t *testing.T) {
	th := NewThrottle()
	if th.AccountFailures("u1") != 0 || th.AccountFailures("") != 0 {
		t.Fatal("unknown account should have 0 failures")
	}
	th.RecordFailure("u1", "192.0.2.1")
	th.RecordFailure("u1", "192.0.2.2")
	if got := th.AccountFailures("u1"); got != 2 {
		t.Fatalf("AccountFailures = %d, want 2", got)
	}
	_ = th.AccountFailures("u1")
	if got := th.AccountFailures("u1"); got != 2 {
		t.Fatalf("reading changed the count: %d", got)
	}
}
