// file: internal/server/handlers/system/handler_health_test.go
// version: 1.0.0
// guid: 6f1b9d30-2c85-4e7a-9a43-b0d8e5c71f26
// last-edited: 2026-10-10

package system_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/system"
	systemmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/system/mocks"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// warmingStore is a SystemStore that also reports memdb warmup state, the way
// *database.PebbleStore does.
type warmingStore struct {
	*systemmocks.MockSystemStore
	ready, done bool
	ms          int64
}

func (w *warmingStore) WarmupStatus() (bool, bool, int64) { return w.ready, w.done, w.ms }

// healthBody serves GET /health against store and returns the raw `data`
// object re-encoded with the volatile timestamp removed, plus the HTTP code.
func healthBody(t *testing.T, store system.SystemStore) (string, int) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := system.New(
		func() system.SystemStore { return store },
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	w := run(http.MethodGet, "/health", "/health", nil, func(r *gin.Engine) {
		r.GET("/health", h.HealthCheck)
	})
	var resp map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	var data map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(resp["data"], &data))
	assert.Contains(t, data, "timestamp")
	delete(data, "timestamp")
	out, err := json.Marshal(data) // map keys marshal sorted: stable golden
	require.NoError(t, err)
	return string(out), w.Code
}

func TestHealthCheck_WarmingReportsNotReadyButStaysOK(t *testing.T) {
	m := systemmocks.NewMockSystemStore(t)
	m.EXPECT().CountAuthors().Return(5, nil)
	body, code := healthBody(t, &warmingStore{MockSystemStore: m, ready: false, done: false})

	assert.Equal(t, http.StatusOK, code, "liveness status must not change while warming")
	assert.JSONEq(t,
		`{"status":"ok","ready":false,"memdb_ready":false,"degraded":["memdb_warming"]}`, body)
	assert.NotContains(t, body, "warmup_ms", "warmup_ms is omitted until warmup is done")
}

func TestHealthCheck_ReadyAfterWarmupReportsDuration(t *testing.T) {
	m := systemmocks.NewMockSystemStore(t)
	m.EXPECT().CountAuthors().Return(5, nil)
	body, code := healthBody(t, &warmingStore{MockSystemStore: m, ready: true, done: true, ms: 1234})

	assert.Equal(t, http.StatusOK, code)
	assert.JSONEq(t,
		`{"status":"ok","ready":true,"memdb_ready":true,"warmup_ms":1234,"degraded":[]}`, body)
}

// A warmup that failed and fell back to Pebble is finished: nothing is left to
// wait for, so ready is true even though the memdb never published.
func TestHealthCheck_FallbackAfterFailedWarmupIsReady(t *testing.T) {
	m := systemmocks.NewMockSystemStore(t)
	m.EXPECT().CountAuthors().Return(5, nil)
	body, _ := healthBody(t, &warmingStore{MockSystemStore: m, ready: false, done: true, ms: 40})

	assert.JSONEq(t,
		`{"status":"ok","ready":true,"memdb_ready":false,"warmup_ms":40,"degraded":[]}`, body)
}

// A store with no warmup to report on has nothing to wait for.
func TestHealthCheck_StoreWithoutWarmupIsReady(t *testing.T) {
	m := systemmocks.NewMockSystemStore(t)
	m.EXPECT().CountAuthors().Return(5, nil)
	body, _ := healthBody(t, m)

	assert.JSONEq(t, `{"status":"ok","ready":true,"memdb_ready":true,"degraded":[]}`, body)
}

// /health is unauthenticated: a store error must surface only as the fixed
// "degraded" status, never as error text, a path, or anything from the store.
func TestHealthCheck_StoreErrorTextNeverAppears(t *testing.T) {
	m := systemmocks.NewMockSystemStore(t)
	m.EXPECT().CountAuthors().Return(0, errors.New("open /srv/secret/path/db: permission denied"))
	body, code := healthBody(t, &warmingStore{MockSystemStore: m, ready: true, done: true, ms: 7})

	assert.Equal(t, http.StatusOK, code)
	assert.JSONEq(t,
		`{"status":"degraded","ready":true,"memdb_ready":true,"warmup_ms":7,"degraded":[]}`, body)
	for _, leak := range []string{"secret", "permission", "/srv", "open "} {
		assert.False(t, strings.Contains(body, leak), "body leaked %q: %s", leak, body)
	}
}
