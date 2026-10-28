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
cooldown, then dropped. A port that is probed again is also judged by who
answers there (see "A port that changes hands while a dispatch waits" below).
The lone-endpoint path keeps the identities it started with, so a worker that
begins to report a machine id during the wait does not make the machine being
waited for look as if another dispatch had forgotten it, and the next
dispatch's own gate moves the memory.

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

#### A worker's machine is resolved by its address, once, and used everywhere

Review of the previous change found the dispatcher naming one machine three ways
inside a single dispatch: the canary memory resolved a worker that missed its
machine id (`u:`) to the machine its URL last reported, but the sick-machine
bench, the condemnation limit, the kills held from a machine, the identity
stored with an answer and a stuck file's condemnation, and the steering of a
struck file away from a machine each used the raw answer. A machine that timed
out on one port was benched on its other ports only if they answered with the
same identity, a lone file stuck on two ports of one machine counted two
machines, and a condemnation recorded under `u:` was not matched against the
same machine answering as `m:` later.

The gate now resolves each worker's identity once, from what the probe read and
what this process knows of the address, and every later use takes that value:
the canary memory, the sick-machine bench, the condemnation limit (counted again
by the same identities when ports are dropped), the kills held, the key a lone
endpoint records its failure under, the identity stored with an answer and with
a condemnation, and the steering. A stored condemnation or strike that names a
raw `u:` or `m:` id still matches the endpoint by that id or by the machine it
resolves to.

#### A worker that cannot report a machine id is never given a machine's memory

A weak identity is bridged to a machine through the address only when the
worker's `/health` reports `backend` `mlx` (`scripts/whisper_mlx_server.py`, the
one server that reports a machine id at all and sometimes misses it in its first
2.5 s). `scripts/whisper_server.py` reports `faster-whisper` and never a machine
id, so a worker of that kind at the address of a remembered machine is another
worker, not that machine: it is not asked the machine's canary clip, and its
address leaves the machine's ports. A refusal now records whether its worker can
report an id, so a refused worker is judged the same way.

#### A failure on a port that answers as `u:` is the machine's failure

A canary failed by a port that answered as `u:`, whose address last reported a
machine, was recorded under the weak key alone. The machine's other ports were
not remembered as sick and were sent a chunk with no canary. The failure is now
recorded on the machine the address last reported.

#### A configured port that has been down for a day stops holding its machine remembered

A machine that failed the canary stays remembered while one of its ports is down,
because the port may come back and would be sent work with no canary. A port left
in the configuration after its tunnel was retired kept the machine remembered for
ever, and a sound machine was asked a canary clip on every dispatch. A port that
has refused every probe for 24 hours (counted from the first refusal of the run;
any answered probe ends the run, and a probe cut short by a cancelled op counts
for nothing) no longer counts as a port of its machine.

#### Smaller changes in the same area

- An entry of the canary memory whose every address is no longer configured is
  dropped with them, not kept for as long as the process lives.
- The helpers only the tests used (`failedCanaryBefore`, `clearCanaryFailed`, the
  package-level `hasUnheardPort`) moved into a `_test.go` file, and the stray doc
  comment above `hasUnheardPort` became its method comment.
- `resolveCanaryKeyLocked` no longer asks the address map first: every address it
  gives a machine is already one of that machine's ports.

#### A refused port forgets its machine, and an old weak strike steers off every port of its machine

A worker that missed its machine id (`u:`) is taken for the machine its address
last reported. That link is now ended by a refused probe: a refused address is the
one moment it can have changed hands (a tunnel that dropped and came back from
another Mac), so a worker that answers as `u:` afterwards stands for itself and
is not credited with the old machine's id, strikes or condemnations. The canary
memory does not depend on the link and still finds a remembered machine through
its ports, and a port that is refused stays a port of its machine until it has
been down 24 hours. A URL that changes hands between two probes that both answer
is not noticed; production runs one server port per Mac, so this is accepted.

A strike stored as `u:` plus the hash of one port, while the machine behind that
port answered as a weak identity, now steers a file away from every port of that
machine and not only the one the hash was made from. A wrong match only steers a
file further away.

The single-Mac tunnel template (`deploy/launchd/com.jdfalk.whisper-tunnel.plist`)
and `scripts/setup_mac_ai_worker.py` host index 0 both use server port 19848, so
a Mac on each race for one port. Both files and the Whisper docs now say so; the
ports are unchanged.

A file struck by a machine is steered away from a port of that machine that
answers as a weak identity after a refused probe, as it already was away from
one the link still named. A wrong match only steers a file further away.

#### A port that changes hands while a dispatch waits is not credited to the machine that used to be behind it

A pool probes a port again once a cooldown it was seen in has ended, and uses it
only if the worker that answers then is the one the dispatch knew when it began.
That covers the wait for a remembered machine's cooldown, the round loop's wait
for a port an earlier dispatch put into cooldown, a stand-in whose cooldown ends
inside the first wait, and a port whose cooldown ends while the dispatch works
other ports or while its own first probe is in flight. Any difference drops the port from the dispatch like
a refusal: one machine for another, a machine's port that was refused and now
answers weakly, a worker that cannot report an id where one could (or the
reverse), a worker that gained a machine id since it was frozen as `u:`, and a
port that could not be read when the dispatch began and reads now, since there
was nothing to compare it with. A port that cannot be read after the wait is not
used either: after a remembered machine's wait it is waited for like a decoder
that is restarting, then dropped (the round loop's wait does the same, below).

A dropped port is asked no canary clip, so its answer cannot forget the machine
that used to be behind it. It is sent no work and given no timeout strike under
that machine's name, which let one machine make a file permanent on its own. It
cannot be counted as a second machine toward a condemnation, and it cannot escape
the bench of the machine it turned out to be. The next dispatch's gate keys it by
the id it reports, and the canary memory of its address follows it. The identity
stored with the condemnation of a file by two machines is that of the worker that
answered at each endpoint. The lone endpoint resolves the memory when it asks,
after the gate has moved the port to the identity it reports, so it keeps a
worker that gained its id and stores both identities (the one the dispatch knew
it by and the machine it reports), so the store can still learn the machine; any
other difference defers its jobs.

The round loop's wait steps by `poolPaceInterval`, as the wait for a remembered
machine does, so a cooldown that another dispatch clears early is noticed at once
and not after the whole cooldown (up to five minutes). Every port seen in a
cooldown (as the dispatch begins, in each step of a wait, and each time the round
loop makes its list of ports that can take work) is probed once after that
cooldown ends, before it takes work or is taken as a stand-in: one `/health` per
port per cooldown end. A port that
cannot be read then is kept out of the work and read again at the later steps of
the wait; it is dropped once the pacing allowance is spent, and while other ports
keep working it simply stays out.

A pool whose last cooldown ended between the round loop making its list of ports
and asking how long to wait used to answer `ErrPoolUnavailable` ("every endpoint
... in cooldown after waiting 6m") having waited for nothing; it now looks again.

A pool left with nothing to send to because its ports changed hands ended as
"refused by capability requirements", which sends an operator hunting for a box
that is not on a GPU. It now ends as the lone endpoint does, wrapping
`errPortChangedHands` (an unrecognised transport error that the ops defer on
without counting toward stopping the run), when at least one port changed hands
and every other refusal is a port that is unreachable, or a port that is stuck.
With stuck ports among them the error names them, but it is not a stuck pool: the
ports that changed hands are not wedged and the next dispatch's gate keys them,
so the ops do not count the page toward their stuck streak. A pool of nothing but
stuck ports is still a stuck pool, a pool of nothing but unreachable ports is
still `ErrPoolUnavailable`, and a capability refusal the wait itself made beside
them keeps the error that names it. Only the refusals made since the dispatch
began are what the error is about: a port the start gate refused for what it
offers (no GPU) was never part of the pool, so it does not turn a pool emptied by
ports that changed hands into "refused by capability requirements".

#### A port that identifies no machine is never counted as one, and only a port that reports a machine's id answers for it

A port whose `/health` could not be read when a dispatch began is frozen as `?`
plus the hash of its URL. It is evidence of no machine, so it is no longer counted
as one toward the number of machines a condemnation needs, with or without a
cooldown wait: one box with a readable port and an unreadable one used to be "two
machines", and a lone file that hung the decoder on both was condemned by "two
workers". The unreadable port still counts as an endpoint, and a job stuck on it
is kept off it, but only the machines it left stuck count toward the verdict
(which names them alone). A pool in which no port could be read knows no machine
and condemns nothing.

A remembered machine is asked its canary clip, and forgotten, only through a port
that reports the machine's own id, or one whose address has answered continuously
since it last reported that id (a worker that lost its id for a moment). A port
that answers weakly (`u:`) or not at all (`?`) is not part of a machine's group
merely because the machine's port list names its address: a refused probe ends
the link of an address to its machine, and the worker that answers there next
stands for itself. It used to be asked the machine's clip on the machine's behalf
and, by passing, forgot a machine that had never been asked, so the machine's
other ports were sent work with no canary. A weak port with no link takes work as
itself and leaves the machine's failure standing; the machine is still asked on
any port that reports its id. The cost is that a machine which restarted behind a
port refused in between takes work on that port, as a worker of its own, until a
port of it reports its id and is asked.

A lone job that hangs the only known machine is no longer condemned on that
machine's word while a port whose machine is unknown (a `?` key) has not been
shown to hang it: that port may be a second machine, so the job is sent to it
first. A request there that merely outlived its ceiling (the worker was not
wedged) is no evidence about the file and does not count as having tried it, so
the outcome does not depend on the order the two ports failed in. If the unknown
port cannot take the job in this dispatch (it is in cooldown, unreadable,
benched, or only overran), the job is not condemned and not struck, and the
dispatch ends as `ErrWorkerStuck`: it counts toward the ops' stuck streak, so a
pool that keeps hitting this stops the run instead of spinning on the same jobs
(only when the unknown port was sent the job and its `/health` could not be read
afterwards is the error a recognised transport failure instead, which the ops
count as an outage). An unknown port is
never waited for through a cooldown, since after the wait it could only be
dropped: the dispatch ends at once and not after the pacing allowance (six
minutes). Once the job has been stuck on that port too the verdict stands,
naming only the machines.

A pool whose split parts could be placed on none of the ports that could take
work ended as a stuck pool when a port's cooldown ended while the second probe
of the round was in flight; it now looks again and probes that port first.

A job's condemnation keeps the machines it was stuck on alone, dropped or not: a
port dropped in the middle of a dispatch (it changed hands, or could not be read
after a wait) still counts toward that job's verdict, while the number of
machines needed is counted again from the ports that are left and can fall to
one. This is as it was and is now stated in the code.

A port whose cooldown ended is now probed again before the canary clip is sent to
it, in both places the clip is sent: the pool-wide canary that runs when every
port has faulted, and the clip a machine that failed it in an earlier dispatch is
asked at the start. Until now a port that faulted in an earlier round, was in
cooldown, and came out of it while a later round ran could be sent the clip
without a probe, and a tunnel that had come back from another Mac was credited
(or blamed) under the machine the dispatch had known. A port that changed hands
is dropped and never asked, one whose `/health` cannot be read is not asked, and
a pool left with no port ends as the pool emptied. The answers of the round in
which this is found are still read before the dispatch ends.

A pool left with only ports it does not wait for (a `?` key) in cooldown, after
a port was dropped in the middle of the dispatch, ends as the pool emptied again
(a deferral the ops do not count as an outage), as it did before unknown ports
stopped being waited for, and not as `ErrPoolUnavailable`. The unavailable error
no longer claims a wait the dispatch did not make; it says how long it slept.

The part-placing wait looks for a port that came out of its cooldown a second
time, after it finds nothing to wait for, so a cooldown that ends between the two
looks no longer ends the dispatch as stuck.

Whether a port needs to be probed again no longer depends on a look at its
cooldown. The pool's health now counts every failure ever marked for an endpoint
(`aidispatch.Health.FailureGeneration`, which a success never takes back), and
the dispatch records, for each port, that count and whether it was in a cooldown
when it last probed it. A port is probed again before it takes work or is sent
the canary clip when it has failed since, or was in a cooldown then. This
catches a cooldown that another dispatch starts and ends inside one round (a 30
second one inside a request of several minutes), which a sample of
`InCooldown` never sees: such a port is no longer sent the clip, and the next
round's work, as the machine it answered as before the cooldown.

A pool left with only ports it does not wait for (`?` keys) in cooldown after a
port was dropped ends as the pool emptied only when a port changed hands. A
port that wedged during the wait beside a port in a long cooldown is an outage
of the pool again (`ErrPoolUnavailable`), not a stuck one, and the "all N
endpoints are stuck" count is of the ports the refusals judged. The ports the
canary's probes drop are dropped after the round's answers are judged, not
before.
