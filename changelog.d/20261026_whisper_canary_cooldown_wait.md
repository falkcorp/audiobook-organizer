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
of `poolPaceInterval`), then asks every port. A cooldown that ends after the
allowance is no verdict, like a canary that could not get a request slot, and is
deferred at once rather than slept through: the dispatch defers the jobs without
counting toward the ops' stop limits, and it is never `ErrPoolUnavailable` for
this case. A cancelled op returns the cancellation from the wait. The lone
endpoint path waits the same way, and its warning reports how long it really
waited.

When another machine that is out of cooldown and whose `/health` was read can
take the work, the remembered machine is set aside, not benched for good: if
that machine then fails its own canary clip and nothing else can work, the
remembered machine is asked (after its cooldown, within the same allowance)
before the dispatch concludes anything, so a pool is never reported as "no
machine answered the canary" about a machine that was never asked. A machine
another dispatch has already forgotten while this one waited is not asked.

After the wait the machine's ports are probed again before the canary is sent.
A port that wedged in the meantime is dropped from the dispatch (and, with
nothing left, the dispatch ends as a stuck pool, not as a sick machine); a port
whose decoder is restarting or that cannot be reached is waited for like a
cooldown, then dropped. That probe judges ports and nothing else: the identities
the dispatch started with stay as they are, so a worker that begins to report a
machine id during the wait does not make the machine being waited for look as if
another dispatch had forgotten it, and the next dispatch's own gate moves the
memory.

#### A whisper machine is forgotten as sick only when every port of it has answered

The lone-endpoint path forgot a remembered machine as soon as the one port that
passed the gate answered the canary clip, although its siblings had been refused
(wedged, decoder restarting, mount unreadable) and had not been asked. When they
came back, the pool sent them work with no question asked. The pool path had the
same hole whenever a sibling was refused. A machine is now forgotten only when
no endpoint the gate refused, and no unprobed endpoint it kept, is a port of it,
so it stays remembered until the next dispatch has asked every port. An
endpoint whose `/health` cannot be read counts as a port of a remembered machine
only if a probe once reported it as one: on a production pool every endpoint sets
`require_gpu`, so an unrelated machine's dead tunnel is refused with no machine
and used to keep every remembered machine remembered for as long as it was down.

#### A failure another dispatch records is not erased by an older answer

Two dispatches asking the same machine could lose an update: one dispatch's pass
cleared the failure the other had just recorded. Each recorded failure now has
its own generation, taken from a counter that never repeats, and a pass forgets
the machine only if no newer failure was recorded since it began to ask. A
preflight canary that fails is recorded again, so a concurrent clear cannot
lose it.

#### Whisper canary failures follow a worker that starts to report a machine id

`canaryFailed` is keyed by machine identity. A worker that reported no
`machine_id` (keyed `u:` plus a hash of its URL) or could not be probed (keyed
`?` plus the same hash) can start to report one (`m:`) inside the same process,
with nothing repaired: the worker retries its machine-id lookup every 30 s after
a first miss. Its old entry named an identity that is never seen again and was
never removed, and dropping it would have forgotten a machine that had just
failed the canary. The dispatcher now moves the entry, with the ports known for
it, to the worker's new identity.

#### The canary memory follows a machine across its ports and identities

Review of the first version found six ways the memory could lose or misplace a
remembered machine, all fixed here.

A machine is now remembered under a stable key even when a concurrent dispatch
moves the identity it was recorded under (`u:`/`?` to `m:`, or back). The record
keeps a url-to-machine map for the last identity a worker reported as a machine
(`m:`), and the check that a machine was "forgotten while this dispatch waited"
asks whether any port of it is still remembered, not whether the original key
is. A weaker identity (no machine id reported, or the probe failed) never takes
a port from a machine: it only joins an entry of its own, or the entry that
already remembers its URL. Before, a port that briefly reported no machine id
could delete itself from the machine's ports, and the machine then looked
forgotten while it was still failing.

A port that was already down or refused with no machine id at the moment its
machine failed the canary is recorded as a port of that machine once the url map
knows it, so it is asked before it takes work, and the machine is not forgotten
while it is unheard.

A job stuck alone on a wedged machine with a remembered machine set aside now
asks that machine (after its cooldown, within the pacing allowance) instead of
ending as "no endpoint is left". The same path already existed for a pool whose
healthy list was empty; it is now also taken when work could not be placed on
the endpoints that were healthy.

Smaller fixes on the same path:

- A cancelled op during the re-probe that follows the wait returns the
  cancellation, not `ErrPoolUnavailable`, on the lone-endpoint path.
- The canary after the wait is sent with the fresh `/health` the re-probe just
  read, not the one read before the wait.
- The wait target is the earliest time at which a remembered machine can be
  asked: the latest cooldown end over that machine's ports, taken as the
  minimum over machines. A target past the allowance defers the jobs at once.
- A stand-in that failed in this dispatch is not waited for.
- A probed endpoint refused for capability (no GPU, decoder restarting) moves
  its memory to its new identity like a kept one does.
- The url-to-machine map and the remembered ports are bounded to the configured
  endpoints, so a retired URL does not hold a machine remembered for ever.
