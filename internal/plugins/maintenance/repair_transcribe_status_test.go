// file: internal/plugins/maintenance/repair_transcribe_status_test.go
// version: 1.0.0
// guid: cf20a915-6b83-4e47-a0d2-38f1e75c9b60
// last-edited: 2026-08-07

package maintenance

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/transcribe"
)

func rsp(s string) *string { return &s }

// TestClassifyStatusRepair pins every branch of the repair decision. These rules
// are the entire safety story: the op rewrites status on ~34k prod rows.
func TestClassifyStatusRepair(t *testing.T) {
	// The verbatim production error shape from the 2026-07-01 outage, with the
	// host address elided — it is fleet-internal and must not enter this repo.
	const transportErr = `transcribe remote: transcribe-batch chunk 0-32: Post "http://<host>/transcribe-batch": dial tcp: connect: connection refused`

	cases := []struct {
		name       string
		book       database.Book
		wantReason string
		wantWrite  bool
		wantStatus *string // nil means "cleared"; only checked when wantWrite
	}{
		{
			// The dominant case: 229 of 230 sampled whisper_error books looked
			// exactly like this — good text from four days before the outage.
			name: "transport_failure_with_good_credits_text_becomes_ok",
			book: database.Book{
				TranscribeStatus:   rsp(statusWhisperError),
				TranscribeError:    rsp(transportErr),
				IntroTranscription: rsp("Dune by Frank Herbert read by Scott Brick"),
			},
			wantReason: repairRecomputedOK, wantWrite: true, wantStatus: rsp(statusOK),
		},
		{
			name: "transport_failure_with_prose_text_becomes_unparsed",
			book: database.Book{
				TranscribeStatus:   rsp(statusWhisperError),
				TranscribeError:    rsp(transportErr),
				IntroTranscription: rsp("Chapter 12 Fury drove through DC. He had a lot on his mind that day."),
			},
			wantReason: repairRecomputedUnparsed, wantWrite: true, wantStatus: rsp(statusUnparsed),
		},
		{
			// 🔴 An unreachable endpoint is NO ATTEMPT MADE. Recording it as a
			// per-file failure blames the file for the network's problem.
			name: "transport_failure_with_no_text_clears_to_never_attempted",
			book: database.Book{
				TranscribeStatus: rsp(statusWhisperError),
				TranscribeError:  rsp(transportErr),
			},
			wantReason: repairClearedNeverTried, wantWrite: true, wantStatus: nil,
		},
		{
			// The sentinel means every retry already returned zero characters.
			// Clearing it would re-queue the book forever at GPU cost.
			name: "silence_sentinel_is_never_cleared",
			book: database.Book{
				TranscribeStatus:   rsp(statusWhisperError),
				TranscribeError:    rsp(transportErr),
				IntroTranscription: rsp(transcribe.SilenceSentinel),
			},
			wantReason: repairSkipSilence,
		},
		{
			// A genuine model failure is an honest record — keep it.
			name: "genuine_model_failure_is_kept",
			book: database.Book{
				TranscribeStatus:   rsp(statusWhisperError),
				TranscribeError:    rsp("CUDA out of memory while decoding segment 3"),
				IntroTranscription: rsp("Dune by Frank Herbert read by Scott Brick"),
			},
			wantReason: repairSkipNotTransport,
		},
		{
			name: "genuine_ffmpeg_failure_is_kept",
			book: database.Book{
				TranscribeStatus: rsp(statusFFmpegError),
				TranscribeError:  rsp("Invalid data found when processing input"),
			},
			wantReason: repairSkipNotTransport,
		},
		{
			name:       "already_ok_is_untouched",
			book:       database.Book{TranscribeStatus: rsp(statusOK), IntroTranscription: rsp("Dune by Frank Herbert")},
			wantReason: repairSkipNotFailed,
		},
		{
			name:       "unset_status_is_untouched",
			book:       database.Book{},
			wantReason: repairSkipNotFailed,
		},
		{
			// A failure status with NO recorded error tells us nothing about the
			// cause, so we must not assume it was the network.
			name:       "failure_with_empty_error_is_kept",
			book:       database.Book{TranscribeStatus: rsp(statusWhisperError)},
			wantReason: repairSkipNotTransport,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyStatusRepair(tc.book)
			if got.Reason != tc.wantReason {
				t.Errorf("Reason = %q, want %q", got.Reason, tc.wantReason)
			}
			if got.Write != tc.wantWrite {
				t.Errorf("Write = %v, want %v", got.Write, tc.wantWrite)
			}
			if !tc.wantWrite {
				return
			}
			switch {
			case tc.wantStatus == nil && got.NewStatus != nil:
				t.Errorf("expected status CLEARED, got %q", *got.NewStatus)
			case tc.wantStatus != nil && got.NewStatus == nil:
				t.Errorf("expected status %q, got cleared", *tc.wantStatus)
			case tc.wantStatus != nil && *got.NewStatus != *tc.wantStatus:
				t.Errorf("status = %q, want %q", *got.NewStatus, *tc.wantStatus)
			}
		})
	}
}

// TestIsTransportFailureIsConservative pins the asymmetry: an unrecognised error
// must be treated as GENUINE and left alone. Wrongly clearing a real failure
// hides a broken file; wrongly keeping one only costs a re-run.
func TestIsTransportFailureIsConservative(t *testing.T) {
	transport := []string{
		`Post "http://host:19847/transcribe-batch": dial tcp: connection refused`,
		"context deadline exceeded",
		"dial tcp 10.0.0.1:80: i/o timeout",
		"lookup whisper.local: no such host",
		"read: connection reset by peer",
		"network is unreachable",
	}
	for _, e := range transport {
		if !isTransportFailure(e) {
			t.Errorf("should be transport: %q", e)
		}
	}

	genuine := []string{
		"CUDA out of memory",
		"Invalid data found when processing input",
		"model failed to load weights",
		"ffmpeg exited with status 1",
		"", // no information at all — never assume the network
		"whisper returned empty text",
		// Must not be caught by a loose "eof" substring — the word appears
		// inside ordinary text and inside file paths.
		"could not decode /library/Neofeud/track01.mp3",
	}
	for _, e := range genuine {
		if isTransportFailure(e) {
			t.Errorf("should NOT be transport (must be kept): %q", e)
		}
	}
}

// TestRepairNeverTouchesTranscriptText is the data-loss guard. The op's whole
// justification is that the TEXT is fine and only the STATUS is wrong, so a
// repair that disturbed text would be strictly worse than doing nothing.
func TestRepairNeverTouchesTranscriptText(t *testing.T) {
	const text = "Dune by Frank Herbert read by Scott Brick"
	b := database.Book{
		TranscribeStatus:    rsp(statusWhisperError),
		TranscribeError:     rsp(`Post "http://h/transcribe-batch": connection refused`),
		IntroTranscription:  rsp(text),
		TranscribedTitle:    rsp("Dune"),
		TranscribedAuthor:   rsp("Frank Herbert"),
		TranscribedNarrator: rsp("Scott Brick"),
	}
	before := *b.IntroTranscription

	v := classifyStatusRepair(b)
	if !v.Write {
		t.Fatal("expected a repair")
	}
	// classifyStatusRepair is pure — it must not have mutated the input at all.
	if *b.IntroTranscription != before {
		t.Errorf("transcript mutated: %q -> %q", before, *b.IntroTranscription)
	}
	if b.TranscribedTitle == nil || *b.TranscribedTitle != "Dune" {
		t.Error("parsed fields must be left alone by a status repair")
	}
	if b.TranscribeStatus == nil || *b.TranscribeStatus != statusWhisperError {
		t.Error("classify must not mutate the book it inspects")
	}
}
