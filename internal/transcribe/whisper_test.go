// file: internal/transcribe/whisper_test.go
// version: 1.0.0
// guid: 0b7e2d9c-3f41-4a8e-9c55-7d1e6a2f8b30
// last-edited: 2026-11-03

package transcribe

import (
	"errors"
	"testing"
)

func TestIntroTextFromBatch(t *testing.T) {
	poolDown := &TransportError{Err: ErrPoolUnavailable}
	cases := []struct {
		name     string
		res      map[string]BatchResult
		err      error
		want     string
		wantErr  bool
		wantDown bool
	}{
		{name: "text", res: map[string]BatchResult{introJobKey: {Text: "  Year One by Bruce Wayne.  "}}, want: "Year One by Bruce Wayne."},
		{name: "batch error wins over partial result", res: map[string]BatchResult{introJobKey: {Text: "x"}}, err: poolDown, wantErr: true, wantDown: true},
		{name: "job error", res: map[string]BatchResult{introJobKey: {Error: "decode failed"}}, wantErr: true},
		{name: "condemned", res: map[string]BatchResult{introJobKey: {Condemned: true, Error: "stuck"}}, wantErr: true},
		{name: "missing", res: map[string]BatchResult{}, wantErr: true},
		{name: "empty text", res: map[string]BatchResult{introJobKey: {Text: "  "}}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := introTextFromBatch(tc.res, tc.err)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("text = %q, want %q", got, tc.want)
			}
			var te *TransportError
			if tc.wantDown && (!errors.As(err, &te) || !IsPoolUnavailable(err)) {
				t.Fatalf("pool outage lost its type through the wrap: %v", err)
			}
		})
	}
}
