### Fixed

#### Whisper dispatcher waits out a cooldown before it asks a remembered machine the canary clip

A machine that failed the canary clip in an earlier dispatch is asked it on
every port before it gets work. When one of its ports was in cooldown, the
dispatcher benched the machine without asking and without waiting, so with the
Mac as the only machine the dispatch ended as `ErrPoolUnavailable` at once. The
book op counts that toward its five-page stop, so six pages in flight could end
the night's run inside one cooldown (at most five minutes) for a Mac that would
have passed the canary afterwards. The dispatch now waits the cooldown out,
bounded by the same pacing allowance as the round loop (`poolPaceMax`, in steps
of `poolPaceInterval`), then asks every port. A cooldown longer than the
allowance is no verdict, like a canary that could not get a request slot: the
dispatch defers the jobs without counting toward the ops' stop limits, and it
is never `ErrPoolUnavailable` for this case. A cancelled op returns the
cancellation from the wait. When another machine that is not known to be sick
is in the pool, the remembered machine is still benched without being asked and
the page does not wait.

#### A whisper machine is forgotten as sick only when every port of it has answered

The lone-endpoint path forgot a remembered machine as soon as the one port that
passed the gate answered the canary clip, although its siblings had been refused
(wedged, decoder restarting, mount unreadable) and had not been asked. When they
came back, the pool sent them work with no question asked. The pool path had the
same hole whenever a sibling was refused. A machine is now forgotten only when
no endpoint the gate refused is, or may be (its `/health` could not be read), a
port of it; until then it stays remembered and the next dispatch asks every port
that is eligible.

#### Stale canary-failure entries no longer accumulate

`canaryFailed` is keyed by machine identity. When a worker that reported no
`machine_id` (keyed `u:` plus a hash of its URL) or could not be probed (keyed
`?` plus the same hash) starts to report one (`m:`), its old entry named an
identity that is never seen again and was never removed. The dispatcher now
drops those entries when it sees the worker's new identity.
