// file: internal/database/pebble_apply_raw_batch_test.go
// version: 1.0.0
// guid: 7d3a1c52-8e94-4b6f-a0d2-5c19e7b48f36
// last-edited: 2026-10-21

package database

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestApplyRawBatch_SetsAndDeletesInOneCommit(t *testing.T) {
	p := newOpChangeTestStore(t)
	require.NoError(t, p.SetRaw("zz:del:1", []byte("old")))
	require.NoError(t, p.SetRaw("zz:keep", []byte("keep")))

	err := p.ApplyRawBatch([]RawWrite{
		{Key: "zz:new:1", Value: []byte("a")},
		{Key: "zz:empty", Value: nil}, // a nil Value is a set of nothing, not a delete
		{Key: "zz:del:1", Delete: true},
		{Key: "zz:dup", Value: []byte("first")},
		{Key: "zz:dup", Value: []byte("last")}, // the later write wins
		{Key: "zz:gone-before-set", Delete: true},
	})
	require.NoError(t, err)

	got, err := p.GetRaw("zz:new:1")
	require.NoError(t, err)
	require.Equal(t, []byte("a"), got)

	got, err = p.GetRaw("zz:empty")
	require.NoError(t, err)
	require.NotNil(t, got, "a nil-valued set must leave an (empty) row")
	require.Empty(t, got)

	got, err = p.GetRaw("zz:del:1")
	require.NoError(t, err)
	require.Nil(t, got, "the delete must have removed the row")

	got, err = p.GetRaw("zz:dup")
	require.NoError(t, err)
	require.Equal(t, []byte("last"), got)

	got, err = p.GetRaw("zz:keep")
	require.NoError(t, err)
	require.Equal(t, []byte("keep"), got, "an untouched key survives")
}

func TestApplyRawBatch_EmptyIsNoOp(t *testing.T) {
	p := newOpChangeTestStore(t)
	require.NoError(t, p.ApplyRawBatch(nil))
	require.NoError(t, p.ApplyRawBatch([]RawWrite{}))
}

// A refused key refuses the WHOLE batch: nothing before it is staged.
func TestApplyRawBatch_RefusedKeyRefusesWholeBatch(t *testing.T) {
	p := newOpChangeTestStore(t)
	require.NoError(t, p.SetRaw("zz:victim", []byte("v")))

	err := p.ApplyRawBatch([]RawWrite{
		{Key: "zz:before", Value: []byte("x")},
		{Key: "zz:victim", Delete: true},
		{Key: opv2OpPrefix + "01TEST", Value: []byte("{}")},
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, errOpv2OpRawKey), "got %v", err)

	got, err := p.GetRaw("zz:before")
	require.NoError(t, err)
	require.Nil(t, got, "a row before the refused key must not have been written")
	got, err = p.GetRaw("zz:victim")
	require.NoError(t, err)
	require.Equal(t, []byte("v"), got, "a delete before the refused key must not have applied")
}
