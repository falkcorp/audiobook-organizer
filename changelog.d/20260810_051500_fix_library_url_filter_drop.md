### Fixed

#### Library filters no longer vanish from the URL right after you apply them

Clicking a Library sidebar filter ("In Progress", "Finished") could throw the
filter straight back out of the address bar. The URL went

    ?page=1  ->  ?search=read_status:finished  ->  ?page=1  ->  ?search=…&page=1

leaving a junk history entry, so Back landed on a URL that had never been
shown, and anything reading the query string directly saw the filter blink out
and return. Reproduced 5 times in 8 runs on WebKit.

`Library.tsx` keeps two effects: one that reads the URL into component state,
one that rebuilds the URL from that state. The writer builds its parameters
from scratch, so it is only ever correct once state has caught up — and it was
firing one render too early. `filters` comes from
`useLibraryFilters({ searchParams, … })`, so it changes identity on the very
render the URL changes, which fired the writer before the reader's `setState`
had landed. It then published a URL derived from the *previous* state, and
because it rebuilds from scratch, every key not in that stale state was
dropped rather than merely stale.

The writer now skips any render on which the query string changed underneath
it and the change was not its own echo. The signal is effect **order**: a
tracker effect declared after the writer advances the "last seen URL" ref, so
within a commit the writer still observes the pre-navigation value. Stamping
that ref from the reader does not work — the reader runs first, so the stamp
lands before the writer checks it and the guard becomes a no-op.

E2E coverage was added for both entry paths — in-app click and deep link — and
the check itself was moved off animation-frame sampling onto history-API
interception, so it observes every URL the app publishes instead of whatever
happened to fall on a frame boundary.
