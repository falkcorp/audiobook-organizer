// file: internal/maintenance/result_test.go
// version: 1.0.0
// guid: fe6dc21e-3b99-4aac-91d3-f046868464a4
// last-edited: 2026-09-19

package maintenance

import (
	"context"
	"errors"
	"testing"
)

func TestSetResult_FailsLoudlyWithoutWriter(t *testing.T) {
	if err := SetResult(context.Background(), map[string]int{"n": 1}); !errors.Is(err, ErrNoResultSetter) {
		t.Fatalf("SetResult without a writer = %v, want ErrNoResultSetter", err)
	}
}

func TestSetResult_ForwardsToWriter(t *testing.T) {
	var got any
	ctx := WithResultSetter(context.Background(), func(v any) error { got = v; return nil })
	if err := SetResult(ctx, "payload"); err != nil || got != "payload" {
		t.Fatalf("SetResult = %v, got %v", err, got)
	}
}
