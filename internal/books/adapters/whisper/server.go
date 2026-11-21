// file: internal/books/adapters/whisper/server.go
// version: 0.1.0
// guid: 520a67a9-c0b0-4e3f-8b1f-eff37a14a6c7
// last-edited: 2026-11-21

package whisper

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/ai"
	"github.com/falkcorp/mediacore/integrations/providerhttp"
	"github.com/falkcorp/mediacore/library"
)

// ServerAdapterID is the adapter of the whisper_server protocol.
const ServerAdapterID integrations.AdapterID = "books.whisper_server"

// DefaultClipSeconds is how much of a file the worker cuts when it reads the
// file itself and the instance sets no clip_seconds: HEAD's IntroSeconds, the
// opening that carries a book's credits.
const DefaultClipSeconds = 30

// requestDeadlineHeader tells the worker how long this call will wait, in whole
// seconds, so a worker that honours it starts no clip nobody is listening for
// (HEAD's internal/transcribe sends the same header).
const requestDeadlineHeader = "X-Whisper-Request-Timeout-S"

// maxAnswer bounds what the adapter reads of any one worker answer. A transcript
// of a thirty second clip is a few hundred bytes; a health body is under a
// kilobyte; a megabyte is a worker that is not one.
const maxAnswer = 1 << 20

// maxErrorBody bounds the excerpt read from a non-200 answer.
const maxErrorBody = 4 << 10

// ServerSettings are a whisper_server instance's settings: the routing
// attributes every ai instance carries (ai.EndpointSettings) and the worker's
// base URL. A worker takes no API key: it is reached on a trusted network (the
// instance is Local, or its host is in core.integrations.egress.lan_allow).
type ServerSettings struct {
	ai.EndpointSettings
	// URL is the worker's base, e.g. http://worker.example:9000. A path prefix is
	// kept, for a worker behind a reverse proxy.
	URL string `json:"url" schema:"format=uri"`
	// ClipSeconds is how much of the start of a file the worker cuts when it is
	// given a path (path delivery); DefaultClipSeconds when 0.
	ClipSeconds int `json:"clip_seconds,omitzero" schema:"minimum=1"`
}

// ServerDescriptor is the whisper_server adapter for Registrar.Adapter. It
// returns an error only if ServerSettings cannot be reflected, which a test of
// this package rules out.
func ServerDescriptor() (integrations.Descriptor, error) {
	schema, err := config.SchemaFor[ServerSettings]()
	if err != nil {
		return integrations.Descriptor{}, fmt.Errorf("whisper: settings schema: %w", err)
	}
	return ai.Adapter(integrations.Meta{
		ID:          ServerAdapterID,
		DisplayName: "Whisper worker (server)",
		Version:     "1.0.0",
		Settings:    schema,
		Egress:      integrations.EgressConfigured,
		// MaxRetries -1: no transport retry. The router owns failover and the
		// bench; a transport that retried a 5xx on the same worker would hide the
		// failure from both, and re-send a whole clip into a worker that holds one
		// inference lock.
		Limits: integrations.Limits{RPS: 10, Burst: 10, MaxRetries: -1, Timeout: callTimeout, Holds: holdPolicy()},
	}, buildServer), nil
}

// serverInstance is one whisper worker.
type serverInstance struct {
	ref         integrations.Ref
	http        integrations.HTTP
	base        string // the URL without a trailing slash
	rootPaths   map[string]string
	clipSeconds int
	log         *slog.Logger // never nil
}

var (
	_ ai.Backend          = (*serverInstance)(nil)
	_ ai.Transcriber      = (*serverInstance)(nil)
	_ integrations.Tester = (*serverInstance)(nil)
)

// buildServer makes an instance. It does no I/O.
func buildServer(_ context.Context, env integrations.Env, cfg integrations.InstanceConfig) (ai.Backend, error) {
	set, err := decodeSettings[ServerSettings](env.Ref, cfg.Settings)
	if err != nil {
		return nil, err
	}
	base, err := workerBase(set.URL)
	if err != nil {
		return nil, &integrations.Error{Ref: env.Ref, Op: "build", Class: integrations.ClassInvalid, Err: err}
	}
	if env.HTTP == nil {
		return nil, &integrations.Error{Ref: env.Ref, Op: "build", Class: integrations.ClassInvalid, Err: errors.New("the instance has no outbound client")}
	}
	clip := set.ClipSeconds
	if clip == 0 {
		clip = DefaultClipSeconds
	}
	return &serverInstance{ref: env.Ref, http: env.HTTP, base: base, rootPaths: set.HostRoots, clipSeconds: clip, log: logOf(env)}, nil
}

// workerBase validates the configured URL and returns it without a trailing
// slash, the form the routes are appended to.
func workerBase(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return "", errors.New("the url setting must be an absolute http or https URL")
	}
	if u.User != nil {
		return "", errors.New("the url setting must not carry credentials")
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", errors.New("the url setting must not carry a query or fragment")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u.String(), nil
}

// Close releases nothing: the connection pool belongs to the instance's HTTP.
func (*serverInstance) Close() error { return nil }

// ---- /health -------------------------------------------------------------------

// workerHealth is the part of the worker's /health body this adapter reads
// (HEAD's internal/transcribe remoteHealth lists every field the dispatcher
// uses; the stuck, deadline and identity ones belong to HEAD's pool and are not
// needed by a single call).
type workerHealth struct {
	// Model is the model the worker loaded; empty from a worker too old to say.
	Model string `json:"model"`
	// Device is the RESOLVED inference device ("cuda", "metal", "cpu", ...).
	Device string `json:"device"`
	// BatchPipeline is a pointer because its ABSENCE is the signal: a pre-1.0.0
	// worker omits the key, and a present false is still a v2 worker.
	BatchPipeline *bool `json:"batch_pipeline"`
	// PathPipeline is true when the worker serves /transcribe-path-batch (a valid
	// WHISPER_PATH_MAP is configured on it); PathRoots is whether each mapped
	// root is readable right now.
	PathPipeline bool       `json:"path_pipeline"`
	PathRoots    []pathRoot `json:"path_roots"`
	// ChildStartError is why the worker's decode process could not start, when
	// it could not; a worker reporting it answers every clip
	// worker_misconfigured.
	ChildStartError string `json:"child_start_error"`
}

// pathRoot is one mapped root's /health entry; Prefix is the path as a caller
// sends it, the form root_paths holds.
type pathRoot struct {
	Prefix   string `json:"prefix"`
	Readable bool   `json:"readable"`
}

// gpuDevices are the devices that name GPU decoding (HEAD's gpuDevices): a
// claim that the backend really offloads decoding, so "cpu", "auto" and "" are
// absent, and a lookalike such as "cpu-fallback-from-cuda" is not matched by a
// substring.
var gpuDevices = map[string]bool{"cuda": true, "metal": true, "mps": true, "rocm": true, "hip": true}

// device is the /health device, normalised the way HEAD reads it: lower case,
// trimmed, without a device-index suffix ("cuda:0" is "cuda").
func (h workerHealth) device() string {
	d := strings.ToLower(strings.TrimSpace(h.Device))
	if i := strings.IndexByte(d, ':'); i >= 0 {
		d = d[:i]
	}
	return d
}

// labels are the capability labels the worker proved it has: the backend by its
// name, "gpu" for any GPU backend, "cpu", and "batch" for a v2 worker. An
// operator cannot declare any of them (the router's measured namespace), which
// is the point: a label typed onto a CPU box would be believed.
func (h workerHealth) labels() []string {
	var out []string
	d := h.device()
	switch {
	case gpuDevices[d]:
		out = append(out, d, "gpu")
	case d == "cpu":
		out = append(out, "cpu")
	}
	if h.BatchPipeline != nil {
		out = append(out, "batch")
	}
	return out
}

// sharedFS reports whether the worker can read library root rootID itself: the
// instance maps the root (rootPath) and the worker's /health lists that path
// under a mapped root that is readable now. A root the instance does not map is
// never shared, so a missing mapping fails closed to uploading the audio.
func (h workerHealth) sharedFS(rootPath string) bool {
	if !h.PathPipeline || rootPath == "" {
		return false
	}
	p := path.Clean(rootPath)
	return slices.ContainsFunc(h.PathRoots, func(r pathRoot) bool {
		prefix := path.Clean(r.Prefix)
		return r.Readable && r.Prefix != "" && (p == prefix || strings.HasPrefix(p, strings.TrimSuffix(prefix, "/")+"/"))
	})
}

// features are the measured ai features: shared_fs:<root> for each mapped root
// the worker reads (in root order, so the answer is stable).
func (i *serverInstance) features(h workerHealth) []ai.Feature {
	var out []ai.Feature
	for _, root := range slices.Sorted(maps.Keys(i.rootPaths)) {
		if h.sharedFS(i.rootPaths[root]) {
			out = append(out, ai.FeatureSharedFS+ai.Feature(root))
		}
	}
	return out
}

// probe is the GET /health behind Caps, Health and Test.
func (i *serverInstance) probe(ctx context.Context, op string) (workerHealth, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, i.base+"/health", nil)
	if err != nil {
		return workerHealth{}, failf(i.ref, op, integrations.ClassInvalid, 0, "", "the url setting does not make a request")
	}
	resp, err := i.send(ctx, op, req)
	if err != nil {
		return workerHealth{}, err
	}
	defer drainClose(resp.Body)
	var h workerHealth
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxAnswer)).Decode(&h); err != nil {
		return workerHealth{}, failf(i.ref, op, integrations.ClassBadOutput, resp.StatusCode, "",
			"the answer to /health is not the worker's JSON; check that the url setting points at a whisper worker")
	}
	return h, nil
}

// Caps measures the worker: its model, its device as labels, the shared_fs
// features its readable roots give, and nothing about locality (the router
// decides it from the URL). It keeps the gate's hold, as the router refreshes
// capabilities on its own schedule.
func (i *serverInstance) Caps(ctx context.Context) (ai.BackendCaps, error) {
	h, err := i.probe(ctx, "caps")
	if err != nil {
		return ai.BackendCaps{}, err
	}
	var caps ai.BackendCaps
	if m := strings.TrimSpace(h.Model); m != "" {
		caps.Models = []string{m}
	}
	caps.Labels = h.labels()
	caps.Features = i.features(h)
	return caps, nil
}

// Health is one /health request. It passes through an active hold
// (providerhttp.WithBypassHold) because the probe is how a hold ends; the
// breaker, the egress guard and the disabled switch still apply. A worker whose
// decoder cannot start, or one of whose mapped roots is unreadable, is
// degraded: it answers, but cannot do everything.
func (i *serverInstance) Health(ctx context.Context) integrations.Health {
	h, err := i.probe(providerhttp.WithBypassHold(ctx), "health")
	if err != nil {
		return healthOf(ctx, err)
	}
	switch {
	case strings.TrimSpace(h.ChildStartError) != "":
		return integrations.Health{Status: integrations.HealthDegraded, Message: "the worker reports that its decoder cannot start"}
	case slices.ContainsFunc(h.PathRoots, func(r pathRoot) bool { return !r.Readable }):
		return integrations.Health{Status: integrations.HealthDegraded, Message: "a root the worker maps is unreadable on the worker (a lost mount?)"}
	}
	return integrations.Health{Status: integrations.HealthOK}
}

// Test is the extra test of Registry.Test: the same probe, through a hold, as an
// operator asked for it.
func (i *serverInstance) Test(ctx context.Context) error {
	_, err := i.probe(providerhttp.WithBypassHold(ctx), "test")
	return err
}

// healthOf is the health a failed probe means.
func healthOf(ctx context.Context, err error) integrations.Health {
	switch integrations.ClassOf(ctx, err) {
	case integrations.ClassAuth:
		return integrations.Health{Status: integrations.HealthAuthFailed, Message: "the worker, or a proxy in front of it, rejected the request"}
	case integrations.ClassRefusedLocal:
		return refusedHealth(err)
	}
	return integrations.Health{Status: integrations.HealthDown, Message: "the worker did not answer usefully"}
}

// refusedHealth is the health of a probe the gate refused before sending. A hold
// or an open breaker is a stand-off the gate set after real failures and lifts
// itself, so the worker is degraded; only the egress guard, a url that names a
// destination this instance may not dial, is a settings problem.
func refusedHealth(err error) integrations.Health {
	if refusal, ok := errors.AsType[*providerhttp.Refusal](err); ok {
		switch refusal.Reason {
		case providerhttp.ReasonHold, providerhttp.ReasonBreaker, providerhttp.ReasonQuota:
			msg := "the worker is standing off after recent failures (" + string(refusal.Reason) + ")"
			if !refusal.Until.IsZero() {
				msg += " until " + refusal.Until.UTC().Format(time.RFC3339)
			}
			return integrations.Health{Status: integrations.HealthDegraded, Message: msg}
		}
	}
	reason, hasReason := providerhttp.ReasonOf(err)
	if (hasReason && reason == providerhttp.ReasonEgress) ||
		errors.Is(err, providerhttp.ErrBlockedAddress) || errors.Is(err, providerhttp.ErrBlockedScheme) ||
		errors.Is(err, providerhttp.ErrInvalidURL) {
		return integrations.Health{Status: integrations.HealthMisconfigured,
			Message: "the egress guard refused the url: it names a private or blocked destination; mark the instance local to allow a LAN worker"}
	}
	return integrations.Health{Status: integrations.HealthDown, Message: "the request was refused before it was sent"}
}

// ---- transcription ---------------------------------------------------------------

// workerAnswer is one clip's answer in a /transcribe or /transcribe-path-batch
// body: its text, or its per-clip error.
type workerAnswer struct {
	Text  string  `json:"text"`
	Error *string `json:"error"`
}

// Transcribe sends one clip. Bytes in req.Audio.Data are uploaded to
// /transcribe; with no bytes, req.Audio.Path is posted to /transcribe-path-batch
// as the worker-side path root_paths[root]/rel and the worker cuts the first
// clip_seconds. The model is the worker's own (it loads one), so the argument
// only travels in Served by the router. The call is attempted once.
func (i *serverInstance) Transcribe(ctx context.Context, _ string, req ai.TranscribeRequest) (ai.TranscribeResponse, error) {
	if err := canceled(ctx, i.ref, "transcribe"); err != nil {
		return ai.TranscribeResponse{}, err
	}
	var (
		ans workerAnswer
		err error
	)
	switch {
	case len(req.Audio.Data) > 0:
		ans, err = i.upload(ctx, req.Audio)
	case req.Audio.Path != (library.Path{}):
		ans, err = i.byPath(ctx, req.Audio.Path)
	default:
		return ai.TranscribeResponse{}, failf(i.ref, "transcribe", integrations.ClassInvalid, 0, "", "the request carries neither audio bytes nor a library path")
	}
	if err != nil {
		return ai.TranscribeResponse{}, err
	}
	if ans.Error != nil && *ans.Error != "" {
		return ai.TranscribeResponse{}, workerError(i.ref, "transcribe", *ans.Error)
	}
	return ai.TranscribeResponse{Text: ans.Text, Usage: ai.Usage{Requests: 1}}, nil
}

// upload posts the clip's bytes to /transcribe.
func (i *serverInstance) upload(ctx context.Context, a ai.AudioSource) (workerAnswer, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	hdr := make(textproto.MIMEHeader)
	hdr.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="clip%s"`, clipExt(a.MIME)))
	hdr.Set("Content-Type", partMIME(a.MIME))
	part, err := mw.CreatePart(hdr)
	if err == nil {
		_, err = part.Write(a.Data)
	}
	if err == nil {
		err = mw.Close()
	}
	if err != nil {
		return workerAnswer{}, failf(i.ref, "transcribe", integrations.ClassInvalid, 0, "", "the upload could not be built")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, i.base+"/transcribe", &body)
	if err != nil {
		return workerAnswer{}, failf(i.ref, "transcribe", integrations.ClassInvalid, 0, "", "the url setting does not make a request")
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	setDeadlineHeader(ctx, req)
	resp, err := i.send(ctx, "transcribe", req)
	if err != nil {
		return workerAnswer{}, err
	}
	defer drainClose(resp.Body)
	var ans workerAnswer
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxAnswer)).Decode(&ans); err != nil {
		return workerAnswer{}, failf(i.ref, "transcribe", integrations.ClassBadOutput, resp.StatusCode, "", "the answer to /transcribe is not the worker's JSON")
	}
	return ans, nil
}

// pathJobKey is the key of the one job a path call sends; the worker echoes
// keys back verbatim.
const pathJobKey = "clip"

// byPath posts one path job to /transcribe-path-batch.
func (i *serverInstance) byPath(ctx context.Context, p library.Path) (workerAnswer, error) {
	if err := p.Validate(); err != nil || p.IsRoot() {
		return workerAnswer{}, failf(i.ref, "transcribe", integrations.ClassInvalid, 0, "", "the library path is not a root-relative file path")
	}
	rootPath, ok := i.rootPaths[string(p.Root)]
	if !ok || rootPath == "" {
		return workerAnswer{}, failf(i.ref, "transcribe", integrations.ClassInvalid, 0, "",
			"the instance maps no path for library root %q (root_paths), so it cannot read the file itself", string(p.Root))
	}
	type wireJob struct {
		Path    string `json:"path"`
		Seconds int    `json:"seconds"`
	}
	raw, err := json.Marshal(map[string]map[string]wireJob{"jobs": {pathJobKey: {Path: path.Join(rootPath, p.Rel), Seconds: i.clipSeconds}}})
	if err != nil {
		return workerAnswer{}, failf(i.ref, "transcribe", integrations.ClassInvalid, 0, "", "the path job could not be encoded")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, i.base+"/transcribe-path-batch", bytes.NewReader(raw))
	if err != nil {
		return workerAnswer{}, failf(i.ref, "transcribe", integrations.ClassInvalid, 0, "", "the url setting does not make a request")
	}
	req.Header.Set("Content-Type", "application/json")
	setDeadlineHeader(ctx, req)
	resp, err := i.send(ctx, "transcribe", req)
	if err != nil {
		return workerAnswer{}, err
	}
	defer drainClose(resp.Body)
	var out struct {
		Results map[string]workerAnswer `json:"results"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxAnswer)).Decode(&out); err != nil {
		return workerAnswer{}, failf(i.ref, "transcribe", integrations.ClassBadOutput, resp.StatusCode, "", "the answer to /transcribe-path-batch is not the worker's JSON")
	}
	ans, ok := out.Results[pathJobKey]
	if !ok {
		return workerAnswer{}, failf(i.ref, "transcribe", integrations.ClassBadOutput, resp.StatusCode, "", "the worker's answer holds no result for the job")
	}
	return ans, nil
}

// setDeadlineHeader tells the worker how long the call will wait, rounded UP to
// a whole second: the worker counts from when its handler starts, which is after
// this call started its clock, so a truncated value would put the worker's
// deadline before the caller's.
func setDeadlineHeader(ctx context.Context, req *http.Request) {
	if dl, ok := ctx.Deadline(); ok {
		if left := time.Until(dl); left > 0 {
			req.Header.Set(requestDeadlineHeader, strconv.FormatInt(int64((left+time.Second-1)/time.Second), 10))
		}
	}
}

// clipExt is the file extension a clip of mimeType is uploaded under; the worker
// decodes by content, so this is a hint for its logs. WAV when unspecified (the
// worker's own extraction format).
func clipExt(mimeType string) string {
	mt, _, _ := mime.ParseMediaType(mimeType)
	switch mt {
	case "", "audio/wav", "audio/x-wav", "audio/wave", "audio/vnd.wave":
		return ".wav"
	}
	if exts, err := mime.ExtensionsByType(mt); err == nil && len(exts) > 0 {
		return slices.Min(exts)
	}
	return ".bin"
}

// partMIME is the part's content type: the caller's, or audio/wav.
func partMIME(mimeType string) string {
	if strings.TrimSpace(mimeType) == "" {
		return "audio/wav"
	}
	return mimeType
}

// ---- transport ------------------------------------------------------------------------

// send makes the request through the instance's gate and returns the answer
// when it is a 200. Any other outcome is a classified *integrations.Error and
// the body is closed. One call is one request: nothing here retries.
func (i *serverInstance) send(ctx context.Context, op string, req *http.Request) (*http.Response, error) {
	resp, err := i.http.Do(req)
	if err != nil {
		return nil, i.fail(ctx, op, err)
	}
	if resp.StatusCode == http.StatusOK {
		return resp, nil
	}
	defer drainClose(resp.Body)
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	return nil, i.statusFailure(op, req.URL.Path, resp, body)
}

// drainClose reads a little of the body, so the connection can be reused, and
// closes it.
func drainClose(b io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(b, 64<<10))
	_ = b.Close()
}

// fail maps a transport error to an *integrations.Error: one that already is
// one (the gate's refusal, the egress guard) keeps its class; a done parent is
// ClassCanceled; the rest goes through integrations.ClassOf.
func (i *serverInstance) fail(ctx context.Context, op string, err error) error {
	if e, ok := errors.AsType[*integrations.Error](err); ok && ctx.Err() == nil {
		if e.Ref == (integrations.Ref{}) || e.Op == "" {
			c := *e
			if c.Ref == (integrations.Ref{}) {
				c.Ref = i.ref
			}
			if c.Op == "" {
				c.Op = op
			}
			return &c
		}
		return e
	}
	class := integrations.ClassOf(ctx, err)
	if class == integrations.ClassUnknown {
		class = integrations.ClassTransient // the request never got an answer
	}
	return &integrations.Error{Ref: i.ref, Op: op, Class: class, Err: errors.New("the worker did not answer")}
}

// statusFailure classifies a non-200 answer. Status classes are core's
// (ai.StatusClass), reading an error body of the shape the OpenAI API and a
// proxy in front of a worker use ({"error":{"type","code"}}) for a quota marker;
// a worker's own FastAPI {"detail": ...} carries none. Two routes differ:
//
//   - A 404 on a route this adapter calls means the url does not reach a whisper
//     worker (ClassBadOutput; the answer is not the worker's), except
//     /transcribe-path-batch, which a worker older than whisper_mlx_server.py
//     1.5.0 does not serve: that instance lacks the capability (ClassUnsupported,
//     so the router benches it and tries another).
//   - 422 is a request the worker refused to read: ClassInvalid, as for any 4xx.
func (i *serverInstance) statusFailure(op, route string, resp *http.Response, body []byte) error {
	typ, code := errorMarkers(body)
	class := ai.StatusClass(resp.StatusCode, typ, code)
	if resp.StatusCode == http.StatusNotFound {
		class = integrations.ClassBadOutput
		if strings.HasSuffix(route, "/transcribe-path-batch") {
			class = integrations.ClassUnsupported
		}
	}
	e := &integrations.Error{Ref: i.ref, Op: op, Class: class, Status: resp.StatusCode, Detail: detail(string(body)),
		Err: fmt.Errorf("the worker answered %d", resp.StatusCode)}
	if s := strings.TrimSpace(resp.Header.Get("Retry-After")); s != "" {
		if secs, err := strconv.Atoi(s); err == nil && secs >= 0 {
			e.RetryAfter = time.Duration(secs) * time.Second
		}
	}
	return e
}

// errorMarkers reads the type and code of an OpenAI-shaped error body; both are
// empty for any other body.
func errorMarkers(body []byte) (typ, code string) {
	var doc struct {
		Error struct {
			Type string `json:"type"`
			Code any    `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &doc) != nil {
		return "", ""
	}
	if s, ok := doc.Error.Code.(string); ok {
		code = s
	}
	return doc.Error.Type, code
}
