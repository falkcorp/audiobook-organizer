### Changed

#### Whisper dispatcher: a test pins that the canary's probes drop changed ports after the round is read

When every whisper endpoint has faulted in a round, the dispatcher probes again
the ports whose cooldown came and went and sends the canary clip only to the
ones that still answer as the machine it knew. A port found answering as another
machine is dropped from the pool, but not until that round's answers have been
judged: they came from the pool as it was when they were sent, and a lone job's
verdict counts machines against it. Two tests now pin this, for a pool that
loses one port and for one that loses every port (it ends as a changed-hands
deferral, not an outage). Two test-only hooks report the pool at the end of the
canary probes and at the top of each pass of the round loop. The health
registry's test clock documents that its restore functions must run in reverse
order of the calls.
