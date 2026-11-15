- [ ] **TODO-F5-11-HOLD-RENEW** Do not register the 14 native group 6 maintenance
      kinds (`books.maintenance.*`, `books.itunes.heal`) until core has `Hold.Renew`
      and the native shells use it. A native apply takes the library-scan stand-down
      once and keeps it for the run, but a lease can lapse in a long recover or
      repoint (HEAD's registry renewed it), and the native heartbeat does not stamp
      liveness either. Acceptance: core `Hold` has a renew call, the native
      stand-down (`t6g6NativeStandDown`) renews while the run lives and stops the
      writer if it lapses, the shells stamp liveness from progress, and a test
      fails when either is removed. F5-18 blocker. Notes: `docs/agent-tasks/platform-core/seams.d/F5-11.md`.
