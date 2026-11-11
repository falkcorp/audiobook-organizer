// file: internal/server/absauth/saturation_test.go
// version: 1.2.0
// guid: 4b91b557-13ea-4dba-b0b6-03391d4ac1bd
// last-edited: 2026-11-10

package absauth

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	coreauth "github.com/falkcorp/mediacore/auth"
)

func TestSaturationPolicy(t *testing.T) {
	tests := []struct {
		failures int
		delay    time.Duration
		refuse   bool
	}{
		{0, SaturationDelayBase, false},
		{1, SaturationDelayBase + SaturationDelayStep, false},
		{coreauth.DefaultSoftThreshold - 1, min(SaturationDelayBase+time.Duration(coreauth.DefaultSoftThreshold-1)*SaturationDelayStep, SaturationDelayMax), false},
		{coreauth.DefaultSoftThreshold, 0, true},
		{coreauth.DefaultSoftThreshold + 10, 0, true},
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

// RefuseSaturatedLogin delays an account under the threshold and refuses (and
// records the refusal on the request) at it, whatever the name.
func TestRefuseSaturatedLogin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var slept []time.Duration
	delay := func(d time.Duration) { slept = append(slept, d) }
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	if RefuseSaturatedLogin(c, 0, delay) || LoginSaturationRefused(c) {
		t.Fatal("a fresh name must be delayed, not refused")
	}
	if len(slept) != 1 || slept[0] != SaturationDelayBase {
		t.Fatalf("slept %v", slept)
	}
	if !RefuseSaturatedLogin(c, coreauth.DefaultSoftThreshold, delay) || !LoginSaturationRefused(c) {
		t.Fatal("a name at the threshold must be refused and the refusal recorded")
	}
	if len(slept) != 1 {
		t.Fatal("a refusal is not delayed")
	}
	other, _ := gin.CreateTestContext(httptest.NewRecorder())
	if LoginSaturationRefused(other) {
		t.Fatal("the flag belongs to one request")
	}
}
