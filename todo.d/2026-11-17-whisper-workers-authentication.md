- [ ] **WHISPER-AUTH** Real authentication for both Whisper workers — neither
      `whisper_mlx_server.py` nor `whisper_server.py` authenticates, and the path
      routes return transcripts of any file under `WHISPER_PATH_MAP`, so the only
      protection is binding loopback and the SSH tunnel. Add a shared-secret header
      checked by the shared module (`scripts/whisper_path_pipeline.py`) on every
      route except `/health`, with the Go whisper client sending it, so a LAN-bound
      worker (`-BindHost 0.0.0.0`) is safe to opt into. Rotate the secret without
      restarting the server.
