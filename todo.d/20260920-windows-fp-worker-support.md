- [ ] **Let `fp-worker` run on Windows.** `internal/fingerprint/workerclient/mount_other.go`
  (`statMount`) and `probe_other.go` (`writeProbe`) have no Windows implementation, so
  the worker refuses to start before leasing a single job — it cannot prove the library
  mount is read-only. Measured 2026-09-20: the box at the `windows-gpu` ssh alias is a
  Ryzen 7 3800X (8C/16T, 64 GB), a real third worker's worth of CPU. Its GPU (Radeon
  R9 200, 2013 silicon, no CUDA and dropped from ROCm) is NOT worth using — CPU only.
  Needs: a Windows `statMount` (drive type + read-only volume flag) and `writeProbe`;
  SMB credentials reachable from an SSH session, since mapped drives show `Unavailable`
  there and only UNC paths work; and a Windows ffmpeg/fpcalc pair that reproduces the
  reference prints byte for byte, which the parity gate will reject if it does not.
  Deferred 2026-09-20 (owner): two Macs cover the current windowed run. Revisit before
  whole-file fingerprints, which is a far bigger job.
