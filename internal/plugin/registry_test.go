// file: internal/plugin/registry_test.go
// version: 1.0.1
// guid: 35cc0160-9d7a-48b8-84bc-6b13d3bc916b
// last-edited: 2026-11-08

package plugin

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testPlugin is a minimal Plugin implementation for testing.
type testPlugin struct {
	id         string
	name       string
	version    string
	caps       []Capability
	initCalled bool
	shutCalled bool
	initErr    error
	healthErr  error
	shutOrder  *[]string // tracks shutdown ordering
	shutErr    error
}

func (p *testPlugin) ID() string                           { return p.id }
func (p *testPlugin) Name() string                         { return p.name }
func (p *testPlugin) Version() string                      { return p.version }
func (p *testPlugin) Capabilities() []Capability           { return p.caps }
func (p *testPlugin) Init(_ context.Context, _ Deps) error { p.initCalled = true; return p.initErr }
func (p *testPlugin) Shutdown(_ context.Context) error {
	p.shutCalled = true
	if p.shutOrder != nil {
		*p.shutOrder = append(*p.shutOrder, p.id)
	}
	return p.shutErr
}
func (p *testPlugin) HealthCheck() error { return p.healthErr }

func newTestPlugin(id string, caps ...Capability) *testPlugin {
	return &testPlugin{id: id, name: id, version: "1.0.0", caps: caps}
}

func freshRegistry() *Registry { return NewRegistry() }

func TestRegistry_RegisterAndGet(t *testing.T) {
	r := freshRegistry()
	p := newTestPlugin("alpha")
	r.plugins[p.ID()] = p

	got, ok := r.Get("alpha")
	require.True(t, ok)
	assert.Equal(t, "alpha", got.ID())
}

func TestRegistry_GetMissing(t *testing.T) {
	r := freshRegistry()
	_, ok := r.Get("nonexistent")
	assert.False(t, ok)
}

func TestRegistry_DuplicateRegistration(t *testing.T) {
	// Use global Register which silently skips duplicates
	ResetForTesting()
	defer ResetForTesting()

	p1 := newTestPlugin("dup-test")
	p2 := newTestPlugin("dup-test")

	Register(p1)
	Register(p2) // should be silently ignored

	all := Global().All()
	assert.Len(t, all, 1)
}

func TestRegistry_EnableDisableIsEnabled(t *testing.T) {
	r := freshRegistry()
	p := newTestPlugin("toggle")
	r.plugins[p.ID()] = p

	assert.False(t, r.IsEnabled("toggle"))

	r.Enable("toggle")
	assert.True(t, r.IsEnabled("toggle"))

	r.Disable("toggle")
	assert.False(t, r.IsEnabled("toggle"))
}

func TestRegistry_InitAllOnlyInitsEnabled(t *testing.T) {
	r := freshRegistry()
	enabled := newTestPlugin("enabled-one")
	disabled := newTestPlugin("disabled-one")

	r.plugins[enabled.ID()] = enabled
	r.plugins[disabled.ID()] = disabled
	r.Enable("enabled-one")

	err := r.InitAll(context.Background(), Deps{})
	require.NoError(t, err)

	assert.True(t, enabled.initCalled)
	assert.False(t, disabled.initCalled)
}

func TestRegistry_InitAllPropagatesError(t *testing.T) {
	r := freshRegistry()
	p := newTestPlugin("fail-init")
	p.initErr = fmt.Errorf("kaboom")

	r.plugins[p.ID()] = p
	r.Enable("fail-init")

	err := r.InitAll(context.Background(), Deps{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "kaboom")
}

func TestRegistry_ShutdownAllReverseOrder(t *testing.T) {
	r := freshRegistry()
	var order []string

	p1 := newTestPlugin("first")
	p1.shutOrder = &order
	p2 := newTestPlugin("second")
	p2.shutOrder = &order
	p3 := newTestPlugin("third")
	p3.shutOrder = &order

	// Manually set initOrder to simulate sequential init
	r.plugins[p1.ID()] = p1
	r.plugins[p2.ID()] = p2
	r.plugins[p3.ID()] = p3
	r.initOrder = []string{"first", "second", "third"}

	require.NoError(t, r.ShutdownAll(context.Background()))

	assert.Equal(t, []string{"third", "second", "first"}, order)
	assert.True(t, p1.shutCalled)
	assert.True(t, p2.shutCalled)
	assert.True(t, p3.shutCalled)
}

// TestRegistry_ShutdownAllReturnsEveryFailure (R1-15 round-2 review, S3): the
// server's stop counts a plugin that did not shut down as an unfinished step,
// so ShutdownAll reports each failure by plugin and still shuts down the rest.
func TestRegistry_ShutdownAllReturnsEveryFailure(t *testing.T) {
	r := NewRegistry()
	scarecrow := newTestPlugin("scarecrow")
	scarecrow.shutErr = context.DeadlineExceeded
	riddler := newTestPlugin("riddler")
	penguin := newTestPlugin("penguin")
	penguin.shutErr = errors.New("umbrella stuck")
	for _, p := range []*testPlugin{scarecrow, riddler, penguin} {
		r.Register(p)
		r.Enable(p.ID())
	}
	require.NoError(t, r.InitAll(context.Background(), Deps{}))

	err := r.ShutdownAll(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Contains(t, err.Error(), "scarecrow: ")
	assert.Contains(t, err.Error(), "penguin: umbrella stuck")
	assert.NotContains(t, err.Error(), "riddler")
	assert.True(t, scarecrow.shutCalled)
	assert.True(t, riddler.shutCalled)
	assert.True(t, penguin.shutCalled)
}

func TestRegistry_ByCapability(t *testing.T) {
	r := freshRegistry()

	dl := newTestPlugin("downloader", CapDownloadClient)
	notif := newTestPlugin("notifier", CapNotifier)
	both := newTestPlugin("multi", CapDownloadClient, CapNotifier)

	r.plugins[dl.ID()] = dl
	r.plugins[notif.ID()] = notif
	r.plugins[both.ID()] = both

	// Enable all
	r.Enable("downloader")
	r.Enable("notifier")
	r.Enable("multi")

	downloaders := r.ByCapability(CapDownloadClient)
	assert.Len(t, downloaders, 2) // downloader + multi

	notifiers := r.ByCapability(CapNotifier)
	assert.Len(t, notifiers, 2) // notifier + multi

	players := r.ByCapability(CapMediaPlayer)
	assert.Len(t, players, 0)
}

func TestRegistry_ByCapabilitySkipsDisabled(t *testing.T) {
	r := freshRegistry()

	p := newTestPlugin("disabled-dl", CapDownloadClient)
	r.plugins[p.ID()] = p
	// Not enabled

	result := r.ByCapability(CapDownloadClient)
	assert.Len(t, result, 0)
}

func TestRegistry_HealthCheckAll(t *testing.T) {
	r := freshRegistry()

	healthy := newTestPlugin("healthy")
	sick := newTestPlugin("sick")
	sick.healthErr = fmt.Errorf("connection refused")

	r.plugins[healthy.ID()] = healthy
	r.plugins[sick.ID()] = sick
	r.initOrder = []string{"healthy", "sick"}

	results := r.HealthCheckAll()
	assert.Len(t, results, 2)
	assert.NoError(t, results["healthy"])
	assert.Error(t, results["sick"])
	assert.Contains(t, results["sick"].Error(), "connection refused")
}

func TestRegistry_HealthCheckAllEmptyWhenNoInit(t *testing.T) {
	r := freshRegistry()
	p := newTestPlugin("uninit")
	r.plugins[p.ID()] = p

	results := r.HealthCheckAll()
	assert.Len(t, results, 0) // no initOrder means no health checks
}

func TestResetForTesting(t *testing.T) {
	ResetForTesting()
	defer ResetForTesting()

	Register(newTestPlugin("will-be-cleared"))
	assert.Len(t, Global().All(), 1)

	ResetForTesting()
	assert.Len(t, Global().All(), 0)
}

func TestRegistry_All(t *testing.T) {
	r := freshRegistry()
	r.plugins["a"] = newTestPlugin("a")
	r.plugins["b"] = newTestPlugin("b")

	all := r.All()
	assert.Len(t, all, 2)
}
