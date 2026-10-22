// file: internal/repairs/main_test.go
// version: 1.0.0
// guid: 721ccd8a-2fde-4c2a-8ce8-e5f22df5803b
// last-edited: 2026-10-22

package repairs

import (
	"os"
	"testing"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/falkcorp/audiobook-organizer/internal/telemetry"
)

// gaugeReader reads the process-wide meter provider this package's tests run
// under. TestMain binds it first, before any test, because the OTel global
// delegates to the FIRST provider set and to no later one. Nothing else in
// this package's tests sets the global.
var gaugeReader = sdkmetric.NewManualReader()

// gaugeTestVersion is the link-time version main would hand to
// telemetry.SetVersion; TestMain sets it before the provider so the test can
// tell a meter built after it from one built at package init.
const gaugeTestVersion = "v0.0.0-gauge-test"

func TestMain(m *testing.M) {
	telemetry.SetVersion(gaugeTestVersion)
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(append([]sdkmetric.Option{sdkmetric.WithReader(gaugeReader)}, telemetry.Views()...)...))
	os.Exit(m.Run())
}
