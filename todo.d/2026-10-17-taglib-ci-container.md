- [ ] **CI-TAGLIB-CONTAINER** Run `test-native-taglib` in a container instead
      of on the u0-local agent's host (owner 2026-10-17: "at some point it might
      be better running it in a container on u0/u1"). Today u0-local runs PR
      code directly on the prod host, behind only a systemd sandbox; the
      remaining exposure is loopback access to prod ports and a persistent
      `/srv/ci` cache. Build a CI image from `Dockerfile.build-cgo`'s
      `go-builder` stage, which already builds TagLib 2.0.2 statically, plus
      ffmpeg and fpcalc for the metafetch tests. Run it on u0's existing
      docker-backend swarm agent, under its per-step CPU and memory caps and
      with only the dedicated cache volume. Then retire u0-local
      (`woodpecker-agent-local.service`, server agent 7, the ZFS dataset
      `nvmecache/woodpecker-local`) and its section in `docs/ci/woodpecker.md`.
      u1 has no docker today, so it would need docker or podman to share the
      lane.
