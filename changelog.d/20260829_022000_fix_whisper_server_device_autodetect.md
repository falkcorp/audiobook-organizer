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

`/health` now also reports the resolved `device` and `compute_type`. This is not
cosmetic. ctranslate2's `get_cuda_device_count()` returns 0 rather than raising
when `cudaGetDeviceCount` fails, so the probe cannot tell "no GPU installed"
from "GPU transiently unavailable" — a driver reset, a TDR, another process
holding the card. Before this change the GPU host would crash on CUDA loss and
its supervisor would restart it, which was loud and self-healing: every restart
re-tried CUDA. With a CPU fallback in place it instead comes up on CPU, serves
healthy 200s about ten times slower, and stays pinned there for the life of the
process. Reporting the device makes that visible to a health check instead of
leaving it in a log line nobody reads. Adding fields is backward compatible —
the Go client decodes into a struct containing only `batch_pipeline` and ignores
unknown fields.

`WHISPER_DEVICE=auto` is now treated as "probe", not as a literal device. It is
a legal ctranslate2 device string, so it would have loaded correctly, but the
Python-level `device` variable would have stayed `"auto"` — picking `int8` on a
working GPU and printing `device=auto` in the banner, which is the same
silently-wrong-banner defect this change set out to fix.

The module docstring said the server must run on a machine with a fast GPU. It
now describes the actual behaviour, including `WHISPER_DEVICE` and the absence
of a ctranslate2 Metal/MPS backend.
