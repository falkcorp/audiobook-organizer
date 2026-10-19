// file: internal/server/password_writers_algo_test.go
// version: 1.0.0
// guid: 0e7d5b38-c1a4-4f92-8d63-b94a27f1c0e6
// last-edited: 2026-10-19

package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every password writer shares one hashing policy (absauth.HashPassword). These
// tests read the stored row back and check it verifies with the algo it records.

func TestAcceptInvite_StoresArgon2idHash(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()

	_, err := s.storeForWiring().CreateInvite(&database.Invite{Token: "tok-argon", Username: "newbie", RoleID: "viewer"})
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/accept-invite", s.handleAcceptInvite)
	body, _ := json.Marshal(map[string]string{"token": "tok-argon", "password": "supersecret"})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/accept-invite", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	user, err := s.storeForWiring().GetUserByUsername("newbie")
	require.NoError(t, err)
	require.NotNil(t, user)
	assert.Equal(t, absauth.AlgoArgon2id, user.PasswordHashAlgo)
	ok, _ := absauth.VerifyPassword(user.PasswordHashAlgo, user.PasswordHash, "supersecret")
	assert.True(t, ok)
}

func TestFindOrCreateAdminUser_StoresArgon2idHash(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()

	user, generated, err := findOrCreateAdminUser(s.storeForWiring())
	require.NoError(t, err)
	require.NotEmpty(t, generated)

	stored, err := s.storeForWiring().GetUserByID(user.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, absauth.AlgoArgon2id, stored.PasswordHashAlgo)
	ok, _ := absauth.VerifyPassword(stored.PasswordHashAlgo, stored.PasswordHash, generated)
	assert.True(t, ok)
}
