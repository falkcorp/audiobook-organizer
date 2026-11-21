// file: web/src/services/coreEvents/clock.test.ts
// version: 0.1.0
// guid: 8b2e4c97-1d35-4f60-a7c8-3e90b5d6a124
// last-edited: 2026-11-20

import { describe, expect, it } from 'vitest';

import { createAwakeClock } from './clock';

function sources() {
  const t = { mono: 5000, wall: 1_800_000_000_000 };
  return {
    t,
    clock: createAwakeClock({ mono: () => t.mono, wall: () => t.wall }),
    /** Both clocks run: an ordinary awake interval. */
    run(ms: number) {
      t.mono += ms;
      t.wall += ms;
    },
    /** The machine sleeps: only the wall clock runs. */
    sleep(ms: number) {
      t.wall += ms;
    },
  };
}

describe('createAwakeClock', () => {
  it('follows the monotonic clock while both run', () => {
    const s = sources();
    const t0 = s.clock();
    s.run(1234);
    expect(s.clock() - t0).toBe(1234);
  });

  it('counts a sleep, in which only the wall clock ran, as elapsed time', () => {
    const s = sources();
    const t0 = s.clock();
    s.run(1000);
    s.sleep(10 * 60 * 1000);
    expect(s.clock() - t0).toBe(1000 + 10 * 60 * 1000);
    s.run(500);
    expect(s.clock() - t0).toBe(1500 + 10 * 60 * 1000);
  });

  it('never goes backwards when the wall clock steps back, and moves no wait', () => {
    const s = sources();
    const t0 = s.clock();
    s.run(1000);
    s.t.wall -= 5 * 60 * 1000;
    expect(s.clock() - t0).toBe(1000);
    s.run(2000);
    expect(s.clock() - t0).toBe(3000);
  });

  it('credits a sleep in full after a step back, not net of the step', () => {
    const s = sources();
    const t0 = s.clock();
    s.t.wall -= 5 * 60 * 1000;
    s.clock();
    s.sleep(10 * 60 * 1000);
    expect(s.clock() - t0).toBe(10 * 60 * 1000);
  });

  it('is non-decreasing across any mix of runs, sleeps and steps', () => {
    const s = sources();
    let last = s.clock();
    for (const step of [
      () => s.run(300),
      () => s.sleep(7000),
      () => (s.t.wall -= 90_000),
      () => s.run(1),
      () => s.sleep(1),
      () => (s.t.wall += 40_000),
      () => s.run(0),
    ]) {
      step();
      const now = s.clock();
      expect(now).toBeGreaterThanOrEqual(last);
      last = now;
    }
  });

  it('defaults to performance.now and Date.now', () => {
    const now = createAwakeClock();
    const a = now();
    const b = now();
    expect(b).toBeGreaterThanOrEqual(a);
    expect(Math.abs(a - performance.now())).toBeLessThan(1000);
  });
});
