- [ ] **PLATFORM-CORE-F3-GATE-STATUS** When F3 moves the Basic gate into
      core's stage 8 and deletes the perimeter shim, check that a request the
      gate refuses on an answer core writes itself (the `/api` redirect, a
      legacy-shape or problem 404, a 405) is logged by stage 4 and counted by
      stage 2 with the 401 the client got. Since R1-17 they record core's own
      status for it (R1-17 deviation 15): the shim wraps the kernel's
      handler, so it rewrites the status after stages 2 and 4 recorded it.
      Accepted until F3, because the earlier fixes cost more: a new exported
      httpapi name for the outer writer to report the status it sent (the
      code contract forbids it), or ABO writing core's internal `State`. Add
      a stage 4 test for the refused case when the gate moves.
