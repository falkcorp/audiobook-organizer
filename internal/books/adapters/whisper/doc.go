// file: internal/books/adapters/whisper/doc.go
// version: 0.1.0
// guid: b396f9d7-80d1-49f9-a146-e595648b072d
// last-edited: 2026-11-21

// Package whisper holds books' two transcription adapters of core's ai kind
// (F7-09 slice T4, 05-ai section 3 "App-side adapters registered by books").
// They speak ABO's own whisper worker protocol, so they are books' and not
// core's:
//
//   - books.whisper_server (ServerDescriptor) is an HTTP worker on the network:
//     GET /health, POST /transcribe (one uploaded clip) and
//     POST /transcribe-path-batch (the worker reads the file itself and cuts
//     the clip). It replaces the whisper_server protocol of HEAD's
//     ai_endpoints rows, whose client half is internal/transcribe/remote.go.
//   - books.whisper_local (LocalDescriptor) is a subprocess on this machine,
//     behind a Runner the caller supplies. It replaces the local_process
//     protocol (HEAD's uv/openai-whisper runner, an OOM-prone last resort), so
//     the instance runs one transcription at a time.
//
// Both implement ai.Backend and ai.Transcriber. The router owns pacing between
// instances, failover and the bench, so neither adapter retries a call: a
// failure is returned once, classified (integrations.Class) so the router's
// failover table acts on it.
//
// Path delivery. A whisper_server instance advertises the measured feature
// ai.FeatureSharedFS+<root> for a library root R when its settings map R to a
// path in root_paths and the worker's /health reports that path under a
// readable path_roots prefix. The router sends ai.AudioSource.Path only to such
// an instance; the adapter then posts the worker-side path root_paths[R]/<rel>
// and the worker cuts the first clip_seconds itself. A lost mount makes the
// feature disappear on the next Caps probe, so audio is uploaded instead (the
// router falls back) rather than every job failing inside a 200.
//
// This package is a layer-0 leaf: it imports core only (not internal/transcribe,
// whose dispatcher, journal and global config stay books'). The package is dark
// until the F7 flip: nothing registers either descriptor, books does through
// Registrar.Adapter at the fold. M6 (internal/books/cutover/m6) already writes
// instances of both adapter IDs.
package whisper
