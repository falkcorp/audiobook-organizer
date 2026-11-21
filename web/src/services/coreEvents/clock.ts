// file: web/src/services/coreEvents/clock.ts
// version: 0.1.0
// guid: 0f6a3b52-84c1-4d7e-9a25-6c1e7d4b8f30
// last-edited: 2026-11-20

// The clock the event stream measures its waits on: monotonic, and awake to a sleep.
//
// performance.now() never steps with the wall clock, but on macOS Chrome and Safari it does
// not advance while the machine sleeps either, so a 10 minute Retry-After recorded before a
// lid close would still be standing 10 minutes after the wake (#4813 r2 m-a). Date.now()
// does advance across the sleep, so the lag between the two is the time the monotonic clock
// missed. This clock is performance.now() plus that lag:
//  - it never goes backwards (the lag only grows), so a wall-clock step back moves no wait;
//  - a sleep counts as time passed, so a wait that elapsed while the lid was shut is spent;
//  - a wall-clock step FORWARD also reads as time passed (it cannot be told from a sleep),
//    so a wait can end early by the size of the step. That is the one place this clock
//    shortens a wait; a step back never stretches one.
//
// One clock is made per EventStream (the manager hands it to every connection), because
// deadlines recorded on one clock mean nothing on another.

export interface ClockSources {
  /** Monotonic ms (performance.now). */
  mono: () => number;
  /** Wall-clock ms (Date.now). */
  wall: () => number;
}

/** A clock in ms that counts time the machine spent asleep; see the file header. */
export function createAwakeClock(sources: Partial<ClockSources> = {}): () => number {
  const mono = sources.mono ?? (() => performance.now());
  const wall = sources.wall ?? (() => Date.now());
  const mono0 = mono();
  let wall0 = wall();
  // Time the monotonic clock has missed so far: only ever grows.
  let lag = 0;
  return () => {
    const m = mono();
    // How far the wall clock has run ahead of the monotonic one since the baseline.
    const ahead = wall() - wall0 - (m - mono0);
    if (ahead > lag) {
      lag = ahead;
    } else if (ahead < lag) {
      // The wall clock stepped back: re-baseline so a later sleep is credited in full
      // from here instead of being offset by the step.
      wall0 += ahead - lag;
    }
    return m + lag;
  };
}
