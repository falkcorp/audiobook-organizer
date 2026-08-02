// file: internal/server/middleware/basicauth_test.go
// version: 1.0.0
// guid: b2c3d4e5-f6a7-8b9c-0d1e-2f3a4b5c6d7e

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/gin-gonic/gin"
)

func setupBasicAuthRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(BasicAuth())
	r.GET("/api/v1/health", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})
	r.GET("/api/v1/audiobooks", func(c *gin.Context) {
		c.String(http.StatusOK, "books")
	})
	r.GET("/assets/main.js", func(c *gin.Context) {
		c.String(http.StatusOK, "js")
	})
	return r
}

func TestBasicAuth_Disabled(t *testing.T) {
	config.AppConfig.BasicAuthEnabled = false

	r := setupBasicAuthRouter()
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/audiobooks", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 when basic auth disabled, got %d", w.Code)
	}
}

func TestBasicAuth_NoCredentials(t *testing.T) {
	config.AppConfig.BasicAuthEnabled = true
	config.AppConfig.BasicAuthUsername = "admin"
	config.AppConfig.BasicAuthPassword = "secret"

	r := setupBasicAuthRouter()
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/audiobooks", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without credentials, got %d", w.Code)
	}
	if w.Header().Get("WWW-Authenticate") == "" {
		t.Error("expected WWW-Authenticate header")
	}
}

func TestBasicAuth_WrongCredentials(t *testing.T) {
	config.AppConfig.BasicAuthEnabled = true
	config.AppConfig.BasicAuthUsername = "admin"
	config.AppConfig.BasicAuthPassword = "secret"

	r := setupBasicAuthRouter()
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/audiobooks", nil)
	req.SetBasicAuth("admin", "wrong")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 with wrong password, got %d", w.Code)
	}
}

func TestBasicAuth_CorrectCredentials(t *testing.T) {
	config.AppConfig.BasicAuthEnabled = true
	config.AppConfig.BasicAuthUsername = "admin"
	config.AppConfig.BasicAuthPassword = "secret"

	r := setupBasicAuthRouter()
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/audiobooks", nil)
	req.SetBasicAuth("admin", "secret")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 with correct credentials, got %d", w.Code)
	}
}

func TestBasicAuth_HealthExempt(t *testing.T) {
	config.AppConfig.BasicAuthEnabled = true
	config.AppConfig.BasicAuthUsername = "admin"
	config.AppConfig.BasicAuthPassword = "secret"

	r := setupBasicAuthRouter()
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/health", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for health endpoint without auth, got %d", w.Code)
	}
}

func TestBasicAuth_StaticAssetsExempt(t *testing.T) {
	config.AppConfig.BasicAuthEnabled = true
	config.AppConfig.BasicAuthUsername = "admin"
	config.AppConfig.BasicAuthPassword = "secret"

	r := setupBasicAuthRouter()
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/assets/main.js", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for static asset without auth, got %d", w.Code)
	}
}
