### Fixed

#### The Whisper server can now run on a machine without an NVIDIA GPU

`scripts/whisper_server.py` hardcoded `device="cuda"`, so it was GPU-only and
died at model load on any other host. That is why the Mac had no Whisper server
running at all while the Go service was configured to call one — every
transcription request went to a host where nothing was listening.

The device is now resolved at startup: `WHISPER_DEVICE` if set, otherwise CUDA
when `ctranslate2.get_cuda_device_count() > 0`, otherwise CPU. Existing CUDA
hosts resolve to `cuda` exactly as before.

`compute_type`'s default had to follow the device. ctranslate2's CPU backend
does not implement `float16`, so leaving the old unconditional `"float16"`
default would have swapped the hardcoded-`cuda` crash for a compute-type crash
and left CPU hosts just as broken. The default is now `float16` on CUDA
(unchanged) and `int8` on CPU; `WHISPER_COMPUTE_TYPE` still overrides both.

ctranslate2 has no Metal/MPS backend, so Apple Silicon runs on CPU. That is
fine in practice — verified end to end on an M1 Max: `small.en` at `int8`
loads in about ten seconds, the batched inference pipeline is available, and a
synthesized clip round-trips correctly through `POST /transcribe`. The
production server reaches it over the LAN (`/health` returns 200).

The startup banner also reported `device=cuda` unconditionally; it now prints
the device actually in use, so a misconfigured host is visible in the log
instead of being silently wrong.
