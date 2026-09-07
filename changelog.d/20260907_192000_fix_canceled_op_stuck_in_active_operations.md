### Fixed

#### A canceled job no longer sits in "Active Operations" forever

A "Transcribe book intros" job that was canceled on 26 June was still showing in
the Activity page's Active Operations panel on 7 September, stuck at 199/200 and
labelled "canceled". Nothing could remove it: the "Clear Stale" button next to it
reported clearing zero, because it looks for jobs that are pending, running or
queued, and this one was none of those.

The cause was a missing timestamp rather than a missing status. Internally a job
is treated as still running if it has no completion time — deliberately, so that
a new kind of ending does not have to be added to a list somewhere before the app
notices it. Cancelling a job that was waiting in the queue set its status but
never wrote that completion time, so it read as finished to the part of the app
that runs jobs and as still going to every part that displays them. A job that
had been interrupted and requeued a few times, as this one had, was the way to
land in that state.

Cancelling now records when it happened, so the job leaves the active panel and
takes its place in the history where it belongs.

Note for anyone with an old stuck entry: this stops new ones appearing, but a job
already in this state keeps its missing timestamp and needs to be repaired
separately.
