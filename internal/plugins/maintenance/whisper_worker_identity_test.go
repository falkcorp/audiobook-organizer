// file: internal/plugins/maintenance/whisper_worker_identity_test.go
// version: 1.0.0
// guid: cdd34ffc-54ed-48e9-8f56-670fdd990a49
// last-edited: 2026-10-26

package maintenance

import (
	"context"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/serverdecode"
	"github.com/falkcorp/audiobook-organizer/internal/transcribe"
)

// The review of #4182: both ops route a file that already carries a timeout
// strike away from the machines that gave it (R4), store the machine's opaque
// identity rather than an endpoint URL (N1, N7), and count decoder_killed as a
// strike (N2). Fixtures are synthetic (Batman titles, 192.0.2.x addresses).

const decoderKilledText = "decoder_killed: the decode process was killed from outside (exit code -9) while on this clip; it was restarted"

func TestBookPage_AFileWithAStrikeAsksThePoolToAvoidTheMachineThatStruckIt(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "")
	fx := newIntroFx(t)
	struck := fx.book("Hush")
	idS, _ := fx.file(struck, "01.m4b", 1, "hush-1", false)
	fx.strike(struck, idS, "hush-1", 1) // stored worker "m:00000100"
	stale := fx.book("Broken City")
	idOld, _ := fx.file(stale, "01.m4b", 1, "bc-1", false)
	fx.strike(stale, idOld, "bc-0", 1) // counted against older bytes: void
	clean := fx.book("Year One")
	fx.file(clean, "01.m4b", 1, "yo-1", false)

	var mu sync.Mutex
	var seen []transcribe.BatchOptions
	swapPathSeams(t, true, "", func(_ context.Context, jobs map[string]transcribe.PathJob, opts transcribe.BatchOptions) (map[string]transcribe.BatchResult, error) {
		mu.Lock()
		round := len(seen)
		seen = append(seen, opts)
		mu.Unlock()
		out := make(map[string]transcribe.BatchResult, len(jobs))
		for id := range jobs {
			out[id] = silentAnswer // the first pass is empty, so the 300 s retry runs
			if round > 0 {
				out[id] = transcribe.BatchResult{Text: goodIntro}
			}
		}
		return out, nil
	})
	fx.bookPage(t, false, struck, stale, clean)

	mu.Lock()
	defer mu.Unlock()
	if len(seen) < 2 {
		t.Fatalf("setup: want the first pass and a retry, got %d dispatches", len(seen))
	}
	first := seen[0].AvoidWorkers
	if first[struck] != "m:00000100" || len(first) != 1 {
		t.Fatalf("first pass AvoidWorkers = %v, want only the struck book, naming its machine", first)
	}
	for i, o := range seen[1:] {
		if o.AvoidWorkers != nil {
			t.Fatalf("retry dispatch %d carries AvoidWorkers %v; only the first pass is struck", i+1, o.AvoidWorkers)
		}
	}
}

func TestFileIntroRun_AJobWithAStrikeCarriesTheMachinesToAvoid(t *testing.T) {
	fx := newIntroFx(t)
	b := fx.book("Dark Victory")
	id, p := fx.file(b, "01.m4b", 1, "dv-1", false)
	fx.whisp.texts[p] = goodIntro
	fx.strike(b, id, "dv-1", 1)
	other := fx.book("Haunted Knight")
	_, po := fx.file(other, "01.m4b", 1, "hk-1", false)
	fx.whisp.texts[po] = goodIntro
	if _, err := fx.run(""); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, call := range fx.whisp.calls {
		for _, j := range call {
			got[j.Path] = j.AvoidWorkers
		}
	}
	if got[p] != "m:00000100" || got[po] != "" {
		t.Fatalf("AvoidWorkers by path = %v, want the struck file to name its machine and the other none", got)
	}

	pj, avoid := fileIntroPathJobs(map[string]fileIntroJob{
		"f1": {Path: p, Seconds: 90, AvoidWorkers: "m:00000100"},
		"f2": {Path: po, Seconds: 90},
	})
	if len(pj) != 2 || pj["f1"].Path != p || avoid["f1"] != "m:00000100" || len(avoid) != 1 {
		t.Fatalf("transport conversion: jobs=%v avoid=%v", pj, avoid)
	}
	res := fileIntroResultsFromBatch(map[string]transcribe.BatchResult{"f1": {Error: decoderTimeoutText, Worker: "m:0000beef", Endpoint: "http://192.0.2.10:8000"}})
	if res["f1"].Worker != "m:0000beef" {
		t.Fatalf("the machine identity was lost converting results: %+v", res["f1"])
	}
}

// N2 (owner decision): a decoder killed from outside is a timeout strike. One
// kill leaves the file undone with one strike; a kill on a second machine makes
// it permanent.
func TestFileIntroRun_ADecoderKillIsAStrikeThatASecondMachineMakesPermanent(t *testing.T) {
	fx := newIntroFx(t)
	b := fx.book("The Man Who Laughs")
	id, p := fx.file(b, "01.m4b", 1, "mwl-1", false)
	fx.whisp.errs[p] = decoderKilledText
	if _, err := fx.runAs(&fileIntroRep{id: "run-1"}, ""); err != nil {
		t.Fatal(err)
	}
	if r := fx.row(b, id); r.IntroTimeouts != 1 || r.TranscribeStatus != nil {
		t.Fatalf("after one kill: strikes=%d status=%v, want one strike and no verdict", r.IntroTimeouts, derefOr(r.TranscribeStatus))
	}
	if _, err := fx.runAs(&fileIntroRep{id: "run-2"}, ""); err != nil {
		t.Fatal(err)
	}
	if r := fx.row(b, id); r.TranscribeStatus == nil || *r.TranscribeStatus != statusTranscribeTimeout {
		t.Fatalf("a kill on a second machine must make it permanent: strikes=%d status=%v", r.IntroTimeouts, derefOr(r.TranscribeStatus))
	}
}

func TestBookPage_ADecoderKillIsAStrikeThatTheSameMachineCannotRepeat(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "")
	fx := newIntroFx(t)
	b := fx.book("Gotham Knights")
	id1, _ := fx.file(b, "01.m4b", 1, "gk-1", false)
	fn, _ := scriptedPath(transcribe.BatchResult{Error: decoderKilledText, Worker: "m:000000aa"})
	swapPathSeams(t, true, "", fn)
	fx.bookPage(t, false, b)
	if r := fx.row(b, id1); r.IntroTimeouts != 1 || r.TranscribeStatus != nil || r.IntroTimeoutWorker != "m:000000aa" {
		t.Fatalf("after one kill: strikes=%d status=%v worker=%q", r.IntroTimeouts, derefOr(r.TranscribeStatus), r.IntroTimeoutWorker)
	}
	fx.bookPage(t, false, b) // the same machine again
	if r := fx.row(b, id1); r.IntroTimeouts != 1 || r.TranscribeStatus != nil {
		t.Fatalf("a second kill on the same machine counted: strikes=%d", r.IntroTimeouts)
	}
	fn, _ = scriptedPath(transcribe.BatchResult{Error: decoderKilledText, Worker: "m:000000bb"})
	swapPathSeams(t, true, "", fn)
	fx.bookPage(t, false, b)
	if r := fx.row(b, id1); r.TranscribeStatus == nil || *r.TranscribeStatus != statusTranscribeTimeout {
		t.Fatalf("a kill on a second machine must make it permanent: %+v", r)
	}
}
