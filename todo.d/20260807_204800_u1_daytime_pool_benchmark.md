<!-- file: todo.d/20260807_204800_u1_daytime_pool_benchmark.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5b3a9e17-2c64-4f80-b1d9-7e05c8a3f264 -->
<!-- last-edited: 2026-08-07 -->

- [ ] **Re-run the CPU-node Whisper benchmark in POOL configuration, during the
      day.** The 2026-08-07 evening run was cut at 20:50 for quiet hours after
      measuring only the single-process shapes. Measured so far (10 real prod
      clips, base.en, beam 5, VAD on, mirroring `scripts/whisper_server.py`):

        1 proc x 48 threads, int8          1.96 clips/min  -> 92 days for tier 3
        1 proc x 48 threads, int8_float32  ~2.04           -> ~89 days

      Two confirmed findings that must drive the config:
      - **int8 buys nothing on this host** (Haswell: no AVX-512/VNNI; ct2 falls
        back to a slow int8 GEMM). Same speed as int8_float32 within noise.
      - **One process cannot use 48 cores** — ctranslate2 plateaus ~8-16
        intra-op threads; the box mostly idled.

      Still to measure: pool shapes **8 workers x 6 threads** and **12 x 4**
      (add to the script; the planned 4 x 12 too), plus single float32 as a
      baseline. Linear-ish scaling at 8 workers would be ~10-14 clips/min
      (~13-18 days for the 260k-file tier-3 tail) — plausible, NOT yet measured,
      do not plan around it until it is.

      Everything is staged on the host: `/opt/whisper-bench/{venv,clips,bench.py}`.
      Rerun: `nohup /opt/whisper-bench/venv/bin/python /opt/whisper-bench/bench.py
      > /opt/whisper-bench/bench.log 2>&1 &` — edit the script first to skip the
      already-measured single-process configs. 🔴 Daytime only; the box is in a
      bedroom. 🔴 Host address is fleet-internal — keep it out of this public
      repo (it is deliberately absent here; see infra-docs).
