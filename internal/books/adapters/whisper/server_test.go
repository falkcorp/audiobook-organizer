// file: internal/books/adapters/whisper/server_test.go
// version: 0.1.0
// guid: ee2de52f-7502-49f8-934e-f5b699aa1c20
// last-edited: 2026-11-21

package whisper

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/ai"
	"github.com/falkcorp/mediacore/library"
)

var wavBytes = append([]byte("RIFF\x24\x00\x00\x00WAVEfmt "), make([]byte, 64)...)

// ---- the descriptor ------------------------------------------------------------------

// Both descriptors carry the routing properties the router needs (an adapter
// that cannot say how it is routed never registers) and a URL-typed url only on
// the server adapter: a local process has no destination.
func TestDescriptors_SchemaAndMeta(t *testing.T) {
	t.Parallel()
	sd, err := ServerDescriptor()
	if err != nil {
		t.Fatal(err)
	}
	m := sd.Meta()
	if m.ID != ServerAdapterID || m.Kind != integrations.KindAI {
		t.Fatalf("server meta = %s / %s", m.ID, m.Kind)
	}
	if p := ai.SettingsProblem(m.Settings); p != "" {
		t.Errorf("the server schema: %s", p)
	}
	if m.Limits.MaxRetries != -1 {
		t.Errorf("MaxRetries = %d, want -1: the router owns failover", m.Limits.MaxRetries)
	}
	var doc struct {
		Properties map[string]struct {
			Format string `json:"format"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(m.Settings.JSON(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Properties["url"].Format != "uri" {
		t.Errorf("the url property is %+v, want format uri", doc.Properties["url"])
	}
	if _, ok := doc.Properties["clip_seconds"]; !ok {
		t.Error("the server schema has no clip_seconds")
	}

	ld, err := LocalDescriptor(&fakeRunner{model: "base.en"})
	if err != nil {
		t.Fatal(err)
	}
	lm := ld.Meta()
	if lm.ID != LocalAdapterID || lm.Kind != integrations.KindAI {
		t.Fatalf("local meta = %s / %s", lm.ID, lm.Kind)
	}
	if p := ai.SettingsProblem(lm.Settings); p != "" {
		t.Errorf("the local schema: %s", p)
	}
	var ldoc struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(lm.Settings.JSON(), &ldoc); err != nil {
		t.Fatal(err)
	}
	if _, ok := ldoc.Properties["url"]; ok {
		t.Error("the local schema has a url property: a local process has no destination")
	}
	if _, err := LocalDescriptor(nil); err == nil {
		t.Error("LocalDescriptor(nil) succeeded")
	}
}

func TestWorkerBase(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		in, want string
		bad      bool
	}{
		{in: "http://192.0.2.7:9000", want: "http://192.0.2.7:9000"},
		{in: "http://192.0.2.7:9000/", want: "http://192.0.2.7:9000"},
		{in: " https://worker.example/whisper// ", want: "https://worker.example/whisper"},
		{in: "ftp://192.0.2.7", bad: true},
		{in: "local:uv-openai-whisper", bad: true},
		{in: "http://robin:Joker0306@192.0.2.7", bad: true},
		{in: "http://192.0.2.7/?token=x", bad: true},
		{in: "http://192.0.2.7/#frag", bad: true},
		{in: "", bad: true},
	} {
		got, err := workerBase(c.in)
		if (err != nil) != c.bad || (!c.bad && got != c.want) {
			t.Errorf("workerBase(%q) = %q, %v; want %q (bad=%v)", c.in, got, err, c.want, c.bad)
		}
	}
}

// ---- Caps, measured -----------------------------------------------------------------------

func capsOf(t *testing.T, health map[string]any, edit ...func(map[string]any)) ai.BackendCaps {
	t.Helper()
	r := newRig(t, edit...)
	r.on("GET /health", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, 200, health) })
	caps, err := r.inst.Caps(context.Background())
	if err != nil {
		t.Fatalf("Caps: %v", err)
	}
	return caps
}

func rootPaths(m map[string]string) func(map[string]any) {
	return func(s map[string]any) { s["root_paths"] = m }
}

func TestCaps_MeasuresModelLabelsAndSharedFS(t *testing.T) {
	t.Parallel()
	caps := capsOf(t, metalWorker("/mnt/gotham"), rootPaths(map[string]string{"books-library": "/mnt/gotham/books", "arkham": "/mnt/gotham/arkham"}))
	if !slices.Equal(caps.Models, []string{"whisper-large-v3-turbo"}) {
		t.Errorf("Models = %v", caps.Models)
	}
	if !slices.Equal(caps.Labels, []string{"metal", "gpu", "batch"}) {
		t.Errorf("Labels = %v, want metal, gpu, batch", caps.Labels)
	}
	want := []ai.Feature{"shared_fs:arkham", "shared_fs:books-library"}
	if !slices.Equal(caps.Features, want) {
		t.Errorf("Features = %v, want %v (sorted, one per readable mapped root)", caps.Features, want)
	}
	if caps.Locality != ai.LocalityAuto {
		t.Errorf("Locality = %q: the adapter proves none, the router decides it from the URL", caps.Locality)
	}
}

// shared_fs is measured, never declared: every way the worker may not read the
// root leaves the feature out, so the router uploads audio instead.
func TestCaps_SharedFSFailsClosed(t *testing.T) {
	t.Parallel()
	mapped := rootPaths(map[string]string{"books-library": "/mnt/gotham/books"})
	for name, c := range map[string]struct {
		health map[string]any
		edit   func(map[string]any)
	}{
		"no mapping on the instance": {health: metalWorker("/mnt/gotham"), edit: nil},
		"worker has no path pipeline": {
			health: map[string]any{"model": "m", "device": "cpu", "path_pipeline": false, "path_roots": []map[string]any{{"prefix": "/mnt/gotham", "readable": true}}},
			edit:   mapped,
		},
		"mount lost": {
			health: map[string]any{"model": "m", "device": "cpu", "path_pipeline": true, "path_roots": []map[string]any{{"prefix": "/mnt/gotham", "readable": false}}},
			edit:   mapped,
		},
		"path outside every prefix": {health: metalWorker("/srv/other"), edit: mapped},
		"prefix is only a string prefix": {
			health: metalWorker("/mnt/gotham/boo"), // /mnt/gotham/books must not match /mnt/gotham/boo
			edit:   mapped,
		},
		"empty prefix": {health: metalWorker(""), edit: mapped},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var edit []func(map[string]any)
			if c.edit != nil {
				edit = append(edit, c.edit)
			}
			if caps := capsOf(t, c.health, edit...); len(caps.Features) != 0 {
				t.Errorf("Features = %v, want none", caps.Features)
			}
		})
	}
	t.Run("the prefix itself is shared", func(t *testing.T) {
		t.Parallel()
		caps := capsOf(t, metalWorker("/mnt/gotham/books/"), mapped)
		if !slices.Equal(caps.Features, []ai.Feature{"shared_fs:books-library"}) {
			t.Errorf("Features = %v", caps.Features)
		}
	})
}

// GPU is a claim the worker proves with its device, matched exactly: a device
// that merely contains a GPU name is not one.
func TestCaps_DeviceLabels(t *testing.T) {
	t.Parallel()
	for device, want := range map[string][]string{
		"cuda:0":                 {"cuda", "gpu"},
		" Metal ":                {"metal", "gpu"},
		"cpu":                    {"cpu"},
		"cpu-fallback-from-cuda": nil,
		"":                       nil,
		"auto":                   nil,
	} {
		caps := capsOf(t, map[string]any{"model": "m", "device": device})
		if !slices.Equal(caps.Labels, want) {
			t.Errorf("device %q: Labels = %v, want %v", device, caps.Labels, want)
		}
	}
	// batch_pipeline present and false is still a v2 worker.
	caps := capsOf(t, map[string]any{"model": "m", "device": "cpu", "batch_pipeline": false})
	if !slices.Equal(caps.Labels, []string{"cpu", "batch"}) {
		t.Errorf("Labels = %v, want cpu, batch", caps.Labels)
	}
}

func TestCaps_AnOldWorkerNamesNoModel(t *testing.T) {
	t.Parallel()
	caps := capsOf(t, map[string]any{})
	if len(caps.Models) != 0 || len(caps.Labels) != 0 || len(caps.Features) != 0 {
		t.Errorf("an empty /health measured %+v", caps)
	}
}

// ---- Health -------------------------------------------------------------------------------

func TestHealth_Statuses(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		status int
		body   any
		want   integrations.HealthStatus
	}{
		"ok":                   {200, metalWorker("/mnt/gotham"), integrations.HealthOK},
		"decoder cannot start": {200, map[string]any{"model": "m", "child_start_error": "no metal"}, integrations.HealthDegraded},
		"a mapped root is lost": {200, map[string]any{"model": "m", "path_pipeline": true,
			"path_roots": []map[string]any{{"prefix": "/a", "readable": true}, {"prefix": "/b", "readable": false}}}, integrations.HealthDegraded},
		"server error": {500, `{"detail":"boom"}`, integrations.HealthDown},
		"rejected":     {401, `{"detail":"no"}`, integrations.HealthAuthFailed},
		"not a worker": {200, "<html>login</html>", integrations.HealthDown},
		"wrong route":  {404, `{"detail":"Not Found"}`, integrations.HealthDown},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			r.on("GET /health", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, c.status, c.body) })
			h := r.inst.Health(context.Background())
			if h.Status != c.want {
				t.Errorf("Health = %+v, want %v", h, c.want)
			}
			if strings.Contains(h.Message, "boom") || strings.Contains(h.Message, "no metal") {
				t.Errorf("the health message carries the worker's text: %q", h.Message)
			}
		})
	}
}

// A hold the gate set after a real failure refuses a transcription, and a health
// probe passes through it: the probe is how the hold ends.
func TestHealth_PassesThroughAHold(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.on("POST /transcribe", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, 503, `{"detail":"warming up"}`) })
	r.on("GET /health", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, 200, metalWorker("/mnt/gotham")) })
	ctx := context.Background()
	_, err := r.inst.Transcribe(ctx, "m", ai.TranscribeRequest{Audio: ai.AudioSource{Data: wavBytes}})
	if c := classOf(err); c != integrations.ClassTransient {
		t.Fatalf("a 503: class %v (%v), want Transient", c, err)
	}
	sent := len(r.requests())
	_, err = r.inst.Transcribe(ctx, "m", ai.TranscribeRequest{Audio: ai.AudioSource{Data: wavBytes}})
	if c := classOf(err); c != integrations.ClassRefusedLocal {
		t.Fatalf("a second call during the hold: class %v (%v), want RefusedLocal", c, err)
	}
	if len(r.requests()) != sent {
		t.Error("a held call still reached the worker")
	}
	if h := r.inst.Health(ctx); h.Status != integrations.HealthOK {
		t.Errorf("Health during a hold = %+v, want OK: the probe passes through", h)
	}
}

// ---- Transcribe: upload --------------------------------------------------------------------

func TestTranscribe_UploadsTheClip(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.on("POST /transcribe", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, 200, map[string]any{"text": " Gotham Gazette, read by Alfred. ", "error": nil, "identity": map[string]any{"model": "m"}})
	})
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	got, err := r.inst.Transcribe(ctx, "whisper-large-v3-turbo", ai.TranscribeRequest{Audio: ai.AudioSource{Data: wavBytes, MIME: "audio/wav"}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != " Gotham Gazette, read by Alfred. " || got.Usage.Requests != 1 {
		t.Errorf("got %+v", got)
	}
	s := r.only()
	if s.Path != "/transcribe" || s.Method != http.MethodPost {
		t.Fatalf("request = %s %s", s.Method, s.Path)
	}
	mt, params, err := mime.ParseMediaType(s.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/form-data" {
		t.Fatalf("content type %q: %v", s.Header.Get("Content-Type"), err)
	}
	part, err := multipart.NewReader(bytes.NewReader(s.Body), params["boundary"]).NextPart()
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(part)
	if part.FormName() != "file" || part.FileName() != "clip.wav" || part.Header.Get("Content-Type") != "audio/wav" || !bytes.Equal(data, wavBytes) {
		t.Errorf("part = field %q file %q type %q, %d bytes (want file/clip.wav/audio/wav/%d)", part.FormName(), part.FileName(), part.Header.Get("Content-Type"), len(data), len(wavBytes))
	}
	secs := s.Header.Get(requestDeadlineHeader)
	if secs != "90" && secs != "89" {
		// Rounded UP from just under 90 s, so 90; a slow runner may lose a second.
		t.Errorf("%s = %q, want the caller's remaining time in whole seconds", requestDeadlineHeader, secs)
	}
}

func TestTranscribe_NoDeadlineNoHeader(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.on("POST /transcribe", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, 200, map[string]any{"text": "x"}) })
	if _, err := r.inst.Transcribe(context.Background(), "m", ai.TranscribeRequest{Audio: ai.AudioSource{Data: wavBytes}}); err != nil {
		t.Fatal(err)
	}
	if h := r.only().Header.Get(requestDeadlineHeader); h != "" {
		t.Errorf("%s = %q with no caller deadline", requestDeadlineHeader, h)
	}
}

func TestClipExt(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"": ".wav", "audio/wav": ".wav", "audio/x-wav": ".wav", "AUDIO/WAV; codecs=1": ".wav",
		"application/x-batsignal-undefined": ".bin",
	} {
		if got := clipExt(in); got != want {
			t.Errorf("clipExt(%q) = %q, want %q", in, got, want)
		}
	}
	if got := clipExt("audio/mpeg"); !strings.HasPrefix(got, ".") || got == ".bin" {
		t.Errorf("clipExt(audio/mpeg) = %q, want an mp3 extension", got)
	}
}

// ---- Transcribe: path delivery ----------------------------------------------------------------

func TestTranscribe_ByPath(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		edit        []func(map[string]any)
		wantSeconds float64
	}{
		"default clip": {edit: nil, wantSeconds: 30},
		"clip_seconds": {edit: []func(map[string]any){func(s map[string]any) { s["clip_seconds"] = 45 }}, wantSeconds: 45},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			edit := append([]func(map[string]any){rootPaths(map[string]string{"books-library": "/mnt/gotham/books"})}, c.edit...)
			r := newRig(t, edit...)
			r.on("POST /transcribe-path-batch", func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, 200, map[string]any{"results": map[string]any{"clip": map[string]any{"text": "Joker's Wild", "error": nil}}})
			})
			got, err := r.inst.Transcribe(context.Background(), "m", ai.TranscribeRequest{Audio: ai.AudioSource{Path: library.Path{Root: "books-library", Rel: "Pennyworth/Alfred/01.m4b"}}})
			if err != nil {
				t.Fatal(err)
			}
			if got.Text != "Joker's Wild" || got.Usage.Requests != 1 {
				t.Errorf("got %+v", got)
			}
			s := r.only()
			if s.Path != "/transcribe-path-batch" || s.Header.Get("Content-Type") != "application/json" {
				t.Fatalf("request = %s %s (%s)", s.Method, s.Path, s.Header.Get("Content-Type"))
			}
			var body struct {
				Jobs map[string]struct {
					Path    string  `json:"path"`
					Seconds float64 `json:"seconds"`
				} `json:"jobs"`
			}
			if err := json.Unmarshal(s.Body, &body); err != nil {
				t.Fatal(err)
			}
			job, ok := body.Jobs["clip"]
			if len(body.Jobs) != 1 || !ok || job.Path != "/mnt/gotham/books/Pennyworth/Alfred/01.m4b" || job.Seconds != c.wantSeconds {
				t.Errorf("jobs = %+v, want one job at the worker-side path for %v s", body.Jobs, c.wantSeconds)
			}
		})
	}
}

// Bytes win when a caller sends both: they are the audio the caller meant, and
// the path may name a root the worker cannot read.
func TestTranscribe_BytesWinOverAPath(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.on("POST /transcribe", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, 200, map[string]any{"text": "bytes"}) })
	got, err := r.inst.Transcribe(context.Background(), "m", ai.TranscribeRequest{Audio: ai.AudioSource{Data: wavBytes, Path: library.Path{Root: "books-library", Rel: "a.m4b"}}})
	if err != nil || got.Text != "bytes" {
		t.Fatalf("got %+v, %v", got, err)
	}
	if r.only().Path != "/transcribe" {
		t.Error("the upload route was not used")
	}
}

// Whatever cannot be sent is refused before anything is: nothing leaves the
// machine for a request the adapter knows is wrong.
func TestTranscribe_RefusesWhatItCannotSend(t *testing.T) {
	t.Parallel()
	mapped := rootPaths(map[string]string{"books-library": "/mnt/gotham/books"})
	for name, c := range map[string]struct {
		audio ai.AudioSource
		edit  []func(map[string]any)
	}{
		"no audio at all":           {audio: ai.AudioSource{}, edit: []func(map[string]any){mapped}},
		"a root the instance lacks": {audio: ai.AudioSource{Path: library.Path{Root: "arkham", Rel: "a.m4b"}}, edit: []func(map[string]any){mapped}},
		"a root mapped to nothing": {audio: ai.AudioSource{Path: library.Path{Root: "books-library", Rel: "a.m4b"}},
			edit: []func(map[string]any){rootPaths(map[string]string{"books-library": ""})}},
		"the root directory itself": {audio: ai.AudioSource{Path: library.Path{Root: "books-library", Rel: ""}}, edit: []func(map[string]any){mapped}},
		"an escaping rel":           {audio: ai.AudioSource{Path: library.Path{Root: "books-library", Rel: "../etc/passwd"}}, edit: []func(map[string]any){mapped}},
		"an absolute rel":           {audio: ai.AudioSource{Path: library.Path{Root: "books-library", Rel: "/etc/passwd"}}, edit: []func(map[string]any){mapped}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, c.edit...)
			_, err := r.inst.Transcribe(context.Background(), "m", ai.TranscribeRequest{Audio: c.audio})
			if got := classOf(err); got != integrations.ClassInvalid {
				t.Errorf("class %v (%v), want Invalid", got, err)
			}
			if n := len(r.requests()); n != 0 {
				t.Errorf("%d request(s) reached the worker", n)
			}
		})
	}
}

// ---- Transcribe: what the worker says -----------------------------------------------------------

func TestTranscribe_WorkerErrorClasses(t *testing.T) {
	t.Parallel()
	for text, want := range map[string]integrations.Class{
		"worker_misconfigured: the decoder cannot start": integrations.ClassTransient,
		"request_abandoned: deadline passed":             integrations.ClassTransient,
		"worker_fault: every worker answered it faulted": integrations.ClassTransient,
		"not_found: no such file":                        integrations.ClassNotFound,
		"path_unmapped: /mnt/x is not mapped":            integrations.ClassInvalid,
		"transcribe_timeout: the decoder looped":         integrations.ClassBadOutput,
		"decoder_crashed: exit 134":                      integrations.ClassBadOutput,
		"decoder_killed: signal 9":                       integrations.ClassBadOutput,
		"ffmpeg_error: invalid data":                     integrations.ClassBadOutput,
		"whisper_error: oops":                            integrations.ClassBadOutput,
		"something nobody recognises":                    integrations.ClassBadOutput,
		"not_found":                                      integrations.ClassNotFound,  // a bare code is still the code
		"not_found something":                            integrations.ClassBadOutput, // no ": ", so the prefix is not a code
	} {
		r := newRig(t)
		r.on("POST /transcribe", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, 200, map[string]any{"text": "", "error": text})
		})
		_, err := r.inst.Transcribe(context.Background(), "m", ai.TranscribeRequest{Audio: ai.AudioSource{Data: wavBytes}})
		e, ok := asError(err)
		if !ok {
			t.Errorf("%q: %T is not an *integrations.Error", text, err)
			continue
		}
		if e.Class != want || e.Ref != gotham || e.Detail == "" {
			t.Errorf("%q: class %v ref %v detail %q, want class %v", text, e.Class, e.Ref, e.Detail, want)
		}
		if _, extra, _ := strings.Cut(text, ": "); extra != "" && strings.Contains(e.Error(), extra) && want != integrations.ClassBadOutput {
			t.Errorf("%q: the error string carries the worker's text: %s", text, e.Error())
		}
	}
}

// An empty error string is no error, and a transcript of silence is an answer:
// the caller reads absence of text as "nothing was said", never as a failure.
func TestTranscribe_EmptyErrorIsNoError(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.on("POST /transcribe", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, 200, `{"text":"","error":""}`)
	})
	got, err := r.inst.Transcribe(context.Background(), "m", ai.TranscribeRequest{Audio: ai.AudioSource{Data: wavBytes}})
	if err != nil || got.Text != "" || got.Usage.Requests != 1 {
		t.Errorf("got %+v, %v", got, err)
	}
}

func TestTranscribe_PathResultMissingIsBadOutput(t *testing.T) {
	t.Parallel()
	r := newRig(t, rootPaths(map[string]string{"books-library": "/mnt/gotham/books"}))
	r.on("POST /transcribe-path-batch", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, 200, map[string]any{"results": map[string]any{"someone-else": map[string]any{"text": "x"}}})
	})
	_, err := r.inst.Transcribe(context.Background(), "m", ai.TranscribeRequest{Audio: ai.AudioSource{Path: library.Path{Root: "books-library", Rel: "a.m4b"}}})
	if c := classOf(err); c != integrations.ClassBadOutput {
		t.Errorf("class %v (%v), want BadOutput", c, err)
	}
}

func TestTranscribe_StatusClasses(t *testing.T) {
	t.Parallel()
	mapped := rootPaths(map[string]string{"books-library": "/mnt/gotham/books"})
	byBytes := ai.AudioSource{Data: wavBytes}
	byPath := ai.AudioSource{Path: library.Path{Root: "books-library", Rel: "a.m4b"}}
	for name, c := range map[string]struct {
		route, pattern string
		audio          ai.AudioSource
		status         int
		body           string
		want           integrations.Class
	}{
		"rate limited":              {"/transcribe", "POST /transcribe", byBytes, 429, `{"detail":"slow down"}`, integrations.ClassRateLimited},
		"quota marker in a proxy":   {"/transcribe", "POST /transcribe", byBytes, 429, `{"error":{"type":"insufficient_quota","code":"insufficient_quota"}}`, integrations.ClassQuota},
		"rejected":                  {"/transcribe", "POST /transcribe", byBytes, 401, `{}`, integrations.ClassAuth},
		"worker down":               {"/transcribe", "POST /transcribe", byBytes, 500, `{}`, integrations.ClassTransient},
		"request refused to read":   {"/transcribe", "POST /transcribe", byBytes, 422, `{"detail":[{"msg":"field required"}]}`, integrations.ClassInvalid},
		"url is not a worker":       {"/transcribe", "POST /transcribe", byBytes, 404, `{}`, integrations.ClassBadOutput},
		"old worker, no path route": {"/transcribe-path-batch", "POST /transcribe-path-batch", byPath, 404, `{"detail":"Not Found"}`, integrations.ClassUnsupported},
		"path route 500":            {"/transcribe-path-batch", "POST /transcribe-path-batch", byPath, 500, `{}`, integrations.ClassTransient},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, mapped)
			r.on(c.pattern, func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, c.status, c.body) })
			_, err := r.inst.Transcribe(context.Background(), "m", ai.TranscribeRequest{Audio: c.audio})
			if got := classOf(err); got != c.want {
				t.Errorf("class %v (%v), want %v", got, err, c.want)
			}
			if e, ok := asError(err); ok && e.Status != c.status {
				t.Errorf("status %d, want %d", e.Status, c.status)
			}
		})
	}
}

func TestTranscribe_RetryAfterIsKept(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.on("POST /transcribe", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "7")
		writeJSON(t, w, 429, `{}`)
	})
	_, err := r.inst.Transcribe(context.Background(), "m", ai.TranscribeRequest{Audio: ai.AudioSource{Data: wavBytes}})
	e, ok := asError(err)
	if !ok || e.Class != integrations.ClassRateLimited || e.RetryAfter != 7*time.Second {
		t.Errorf("got %+v", e)
	}
}

// A body that does not decode is the worker's problem, named for what it is, and
// the excerpt goes to Detail only, bounded and on one line.
func TestTranscribe_GarbageBodies(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"html":      "<html>login</html>",
		"truncated": `{"text":"abc`,
		"empty":     "",
	} {
		r := newRig(t)
		r.on("POST /transcribe", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, 200, body) })
		_, err := r.inst.Transcribe(context.Background(), "m", ai.TranscribeRequest{Audio: ai.AudioSource{Data: wavBytes}})
		if c := classOf(err); c != integrations.ClassBadOutput {
			t.Errorf("%s: class %v (%v), want BadOutput", name, c, err)
		}
	}
	// Detail is bounded and single-line.
	r := newRig(t)
	r.on("POST /transcribe", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, 500, "line one\nline two\x00"+strings.Repeat("é", 2000))
	})
	_, err := r.inst.Transcribe(context.Background(), "m", ai.TranscribeRequest{Audio: ai.AudioSource{Data: wavBytes}})
	e, _ := asError(err)
	if e == nil || len(e.Detail) > integrations.MaxDetail || strings.ContainsAny(e.Detail, "\n\x00") || !strings.HasPrefix(e.Detail, "line one line two") {
		t.Errorf("Detail = %q (%d bytes)", e.Detail, len(e.Detail))
	}
}

func TestTranscribe_CanceledCallerSendsNothing(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := r.inst.Transcribe(ctx, "m", ai.TranscribeRequest{Audio: ai.AudioSource{Data: wavBytes}})
	if c := integrations.ClassOf(ctx, err); c != integrations.ClassCanceled {
		t.Errorf("class %v (%v), want Canceled", c, err)
	}
	if n := len(r.requests()); n != 0 {
		t.Errorf("%d request(s) reached the worker", n)
	}
}

// The instance is shared by every call the router makes at once; the adapter
// holds no per-call state.
func TestTranscribe_ConcurrentCallsAreIndependent(t *testing.T) {
	t.Parallel()
	r := newRig(t, rootPaths(map[string]string{"books-library": "/mnt/gotham/books"}))
	r.on("POST /transcribe", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, 200, map[string]any{"text": "bytes"}) })
	r.on("POST /transcribe-path-batch", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, 200, map[string]any{"results": map[string]any{"clip": map[string]any{"text": "path"}}})
	})
	var wg sync.WaitGroup
	errs := make(chan string, 32)
	for n := range 16 {
		wg.Go(func() {
			a, want := ai.AudioSource{Data: wavBytes}, "bytes"
			if n%2 == 1 {
				a, want = ai.AudioSource{Path: library.Path{Root: "books-library", Rel: "a.m4b"}}, "path"
			}
			got, err := r.inst.Transcribe(context.Background(), "m", ai.TranscribeRequest{Audio: a})
			if err != nil || got.Text != want {
				errs <- want + ": " + got.Text
			}
		})
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}

func asError(err error) (*integrations.Error, bool) {
	var e *integrations.Error
	if err == nil {
		return nil, false
	}
	for ; err != nil; err = unwrap(err) {
		if x, ok := err.(*integrations.Error); ok {
			e = x
			break
		}
	}
	return e, e != nil
}

func unwrap(err error) error {
	u, ok := err.(interface{ Unwrap() error })
	if !ok {
		return nil
	}
	return u.Unwrap()
}
