// file: internal/util/pointers_test.go
// version: 1.0.3
// guid: a19ad7f3-3545-4392-b1f3-99f8de3c9aa5
// last-edited: 2026-10-18

package util

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStringPtr(t *testing.T) {
	p := new("hello")
	assert.NotNil(t, p)
	assert.Equal(t, "hello", *p)
}

func TestIntPtr(t *testing.T) {
	p := new(42)
	assert.NotNil(t, p)
	assert.Equal(t, 42, *p)
}

func TestBoolPtr(t *testing.T) {
	p := new(true)
	assert.NotNil(t, p)
	assert.True(t, *p)
}

func TestExtractStringField(t *testing.T) {
	m := map[string]any{"name": "foo", "count": 3.0}
	s, ok := ExtractStringField(m, "name")
	assert.True(t, ok)
	assert.Equal(t, "foo", s)

	_, ok = ExtractStringField(m, "missing")
	assert.False(t, ok)
}

func TestExtractIntField(t *testing.T) {
	m := map[string]any{"count": 42.0}
	i, ok := ExtractIntField(m, "count")
	assert.True(t, ok)
	assert.Equal(t, 42, i)
}

func TestExtractBoolField(t *testing.T) {
	m := map[string]any{"active": true}
	b, ok := ExtractBoolField(m, "active")
	assert.True(t, ok)
	assert.True(t, b)
}
