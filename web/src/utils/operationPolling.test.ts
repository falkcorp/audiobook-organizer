// file: web/src/utils/operationPolling.test.ts
// version: 1.1.0
// guid: 7c2e5a19-4b83-4d06-9f71-2ae8d5c30b14
// last-edited: 2026-09-12

import { describe, expect, it } from 'vitest';

import { isInterrupted, isRetryable, isTerminal } from './operationPolling';

// isRetryable is what the Activity page offers Retry for; every status it
// accepts must be one the server's retry endpoint accepts (isRetryableV2Status)
// — the bare legacy "interrupted" used to be offered here and 409'd there.
describe('isRetryable / isInterrupted', () => {
  it.each([
    'interrupted',
    'interrupted_quiesced',
    'interrupted_dropped',
    'interrupted_restart',
    'interrupted_ask',
  ])('%s is interrupted and retryable (resumed in place)', (status) => {
    expect(isInterrupted(status)).toBe(true);
    expect(isRetryable(status)).toBe(true);
  });

  it.each(['failed', 'canceled'])('%s is retryable as a new run, not interrupted', (status) => {
    expect(isRetryable(status)).toBe(true);
    expect(isInterrupted(status)).toBe(false);
  });

  it.each(['completed', 'queued', 'running', 'waiting_deps', 'interruptedx'])(
    '%s is not offered Retry',
    (status) => {
      expect(isRetryable(status)).toBe(false);
    }
  );
});

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
