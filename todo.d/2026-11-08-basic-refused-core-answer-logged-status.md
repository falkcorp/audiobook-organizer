- [ ] **PLATFORM-CORE-F3-GATE-STATUS** When F3 moves the Basic gate into
      core's stage 8 and deletes the perimeter shim, check that a request the
      gate refuses on an answer core writes itself (the `/api` redirect, a
      legacy-shape or problem 404, a 405) is logged by stage 4 and counted by
      stage 2 with the 401 the client got. Since R1-17 they record core's own
      status for it (R1-17 deviation 15): the shim wraps the kernel's
      handler, so it rewrites the status after stages 2 and 4 recorded it.
      The byte count is core's too, although the client got none of core's
      body: stage 2's `http.server.response.body.size` histogram and the
      span's `http.response.body.size`, and stage 4's `bytes`.
      Accepted until F3, because the earlier fixes cost more: a new exported
      httpapi name for the outer writer to report the status it sent (the
      code contract forbids it), or ABO writing core's internal `State`. Add
      a stage 4 test for the refused case when the gate moves. The shim
      also gives the gate's 401 back the `Vary: Origin` that stage 9 deletes
      under gzip; once the gate is stage 8, its 401 must keep
      `Vary: Origin` whenever stage 6 allowed the origin (the "gated, gzip"
      cases of `TestPerimeter_ParityWithHEAD` check it).
