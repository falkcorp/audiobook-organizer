// file: internal/server/absauth/saturation_test.go
// version: 1.3.0
// guid: 4b91b557-13ea-4dba-b0b6-03391d4ac1bd
// last-edited: 2026-11-10

package absauth

import (
	"net/http/httptest"
	"testing"
	"time"

	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/gin-gonic/gin"
)

// TestSaturationPolicy moved to core/auth/ceiling_test.go with the policy (F4-02).

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
	if len(slept) != 1 || slept[0] != coreauth.SaturationDelayBase {
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
