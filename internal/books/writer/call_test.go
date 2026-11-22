// file: internal/books/writer/call_test.go
// version: 0.1.0
// guid: 7a1d5c36-e082-4b94-9f27-6c3b0e8d4a51
// last-edited: 2026-11-22

package writer_test

import (
	"context"
	"errors"
	"testing"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/books/writer"
	"github.com/falkcorp/audiobook-organizer/internal/books/writer/writertest"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

func oneRow() writer.Rows { return writer.Rows{"M": {ops.ResBooks}} }

func TestGate_ErrAndValueRunTheCallLiveAndNotWhenRefused(t *testing.T) {
	ctx := context.Background()
	subject := writer.Ref(string(ops.ResBooks), "b1")
	declared := tasks.Writes(tasks.Res(writer.Module, string(ops.ResBooks))).WriteSet()

	t.Run("live", func(t *testing.T) {
		g := writer.New(writertest.ForSet("k", declared, tasks.ModeLive), oneRow())
		ran := 0
		require.NoError(t, g.Err(ctx, "M", subject, func() error { ran++; return nil }))
		v, err := writer.Value(ctx, g, "M", subject, func() (int, error) { ran++; return 7, nil })
		require.NoError(t, err)
		require.Equal(t, 7, v)
		require.Equal(t, 2, ran)

		boom := errors.New("boom")
		v, err = writer.Value(ctx, g, "M", subject, func() (int, error) { return 3, boom })
		require.ErrorIs(t, err, boom)
		require.Equal(t, 3, v, "a call that ran and failed keeps what it returned")
	})
	t.Run("outside the declared effects", func(t *testing.T) {
		g := writer.New(writertest.ForSet("k", nil, tasks.ModeLive), oneRow())
		ran := 0
		require.ErrorIs(t, g.Err(ctx, "M", subject, func() error { ran++; return nil }), tasks.ErrOutsideEffects)
		v, err := writer.Value(ctx, g, "M", subject, func() (int, error) { ran++; return 7, nil })
		require.ErrorIs(t, err, tasks.ErrOutsideEffects)
		require.Zero(t, v)
		require.Zero(t, ran, "a refusal never runs the call")
	})
	t.Run("no row", func(t *testing.T) {
		g := writer.New(writertest.ForSet("k", declared, tasks.ModeLive), oneRow())
		require.ErrorIs(t, g.Err(ctx, "Nope", subject, func() error { t.Fatal("ran"); return nil }), writer.ErrNoRow)
	})
}

func TestForRun_NilRunWriterRefusesInsteadOfPanicking(t *testing.T) {
	g := writer.ForRun(nil, oneRow())
	ran := false
	err := g.Err(context.Background(), "M", writer.Ref(string(ops.ResBooks), "b1"), func() error { ran = true; return nil })
	require.ErrorIs(t, err, tasks.ErrOutsideEffects)
	require.False(t, ran)
}

func TestRef(t *testing.T) {
	require.Equal(t, tasks.Res(writer.Module, "books"), writer.Ref("books", ""))
	require.Equal(t, tasks.ResID(writer.Module, "books", "b1"), writer.Ref("books", "b1"))
}
