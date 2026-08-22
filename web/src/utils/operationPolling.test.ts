// file: web/src/utils/operationPolling.test.ts
// version: 1.0.0
// guid: 7c2e5a19-4b83-4d06-9f71-2ae8d5c30b14
// last-edited: 2026-08-22

import { describe, expect, it } from 'vitest';

import { isTerminal } from './operationPolling';

describe('isTerminal', () => {
  it.each(['completed', 'failed', 'canceled'])('treats %s as terminal', (status) => {
    expect(isTerminal(status)).toBe(true);
  });

  // The regression this guards. Every ResumePolicy except ResumeDrop ends a
  // restart-interrupted op at interrupted_quiesced; ResumeDrop (which
  // itunes.import uses) ends at interrupted_dropped. A poller that enumerates
  // only completed/failed/canceled re-arms its timer forever on an op that has
  // already finished — the UI spins with a progress bar that never moves.
  it.each([
    'interrupted',
    'interrupted_quiesced',
    'interrupted_dropped',
    'interrupted_restart',
    'interrupted_ask',
  ])('treats %s as terminal', (status) => {
    expect(isTerminal(status)).toBe(true);
  });

  // Prefix matching must not swallow a future non-terminal status that merely
  // starts with the same letters, so the boundary is the underscore.
  it('does not treat a look-alike status as terminal', () => {
    expect(isTerminal('interrupting')).toBe(false);
    expect(isTerminal('interruptedly')).toBe(false);
  });

  it.each(['queued', 'running', 'pending', ''])('treats %s as non-terminal', (status) => {
    expect(isTerminal(status)).toBe(false);
  });
});
