// file: web/src/utils/followOperation.test.ts
// version: 1.1.0
// guid: 8e1b4c27-6a39-4d50-9f82-0c5d7a3b1e64
// last-edited: 2026-10-22

import { describe, expect, it, vi } from 'vitest';
import { ApiError } from '../services/api';
import { ApiAuthRedirectError, ApiTimeoutError } from './apiFetch';
import {
  OperationLostError,
  classifyFollowError,
  describeFollowTrouble,
  followOperation,
  isOperationLostError,
} from './followOperation';

const noWait = () => Promise.resolve();

describe('followOperation', () => {
  it('returns the first successful poll without saying anything', async () => {
    const onTrouble = vi.fn();
    const poll = vi.fn().mockResolvedValue('done');
    await expect(followOperation(poll, { onTrouble, sleep: noWait })).resolves.toBe('done');
    expect(onTrouble).not.toHaveBeenCalled();
  });

  it('keeps polling through failed polls, says "trouble" once, and returns the terminal state', async () => {
    const onTrouble = vi.fn();
    const onRecovered = vi.fn();
    const poll = vi
      .fn()
      .mockRejectedValueOnce(new Error('network down'))
      .mockRejectedValueOnce(new Error('still down'))
      .mockRejectedValueOnce(new Error('and again'))
      .mockResolvedValue('completed');
    await expect(followOperation(poll, { onTrouble, onRecovered, sleep: noWait })).resolves.toBe(
      'completed'
    );
    expect(poll).toHaveBeenCalledTimes(4);
    expect(onTrouble).toHaveBeenCalledTimes(1);
    expect(onTrouble.mock.calls[0][0]).toEqual(new Error('network down'));
    expect(onRecovered).toHaveBeenCalledTimes(1);
  });

  it('backs off exponentially up to the cap', async () => {
    const waits: number[] = [];
    let n = 0;
    const poll = vi.fn(async () => {
      if (n++ < 7) throw new Error('down');
      return 'ok';
    });
    await followOperation(poll, {
      baseDelayMs: 1000,
      maxDelayMs: 10000,
      sleep: async (ms) => void waits.push(ms),
    });
    expect(waits).toEqual([1000, 2000, 4000, 8000, 10000, 10000, 10000]);
  });

  it('stops only when the page goes away (abort), and rethrows the abort', async () => {
    const ac = new AbortController();
    const poll = vi.fn(async () => {
      ac.abort();
      throw new DOMException('aborted', 'AbortError');
    });
    await expect(followOperation(poll, { signal: ac.signal, sleep: noWait })).rejects.toMatchObject(
      { name: 'AbortError' }
    );
    expect(poll).toHaveBeenCalledTimes(1);
  });

  it('says trouble again for a second outage after a recovery', async () => {
    const onTrouble = vi.fn();
    const seq = [new Error('a'), 'running', new Error('b'), 'done'];
    let finished = false;
    const results: string[] = [];
    // The caller loops until terminal, as pollOperationV2 does inside one poll.
    const poll = vi.fn(async () => {
      const next = seq.shift();
      if (next instanceof Error) throw next;
      results.push(String(next));
      if (next === 'done') finished = true;
      return next as string;
    });
    while (!finished) {
      await followOperation(poll, { onTrouble, sleep: noWait });
    }
    expect(results).toEqual(['running', 'done']);
    expect(onTrouble).toHaveBeenCalledTimes(2);
  });
});

// Review round 3 S-4: waiting cannot fix a 404/410/401/403, so following stops
// with "outcome unknown" instead of polling forever with the books locked;
// timeouts, 5xx and network errors stay retryable.
describe('followOperation terminal and retryable failures', () => {
  it.each([404, 410, 401, 403])('a %i is terminal: it rejects with OperationLostError after one poll', async (status) => {
    const onTrouble = vi.fn();
    const poll = vi.fn().mockRejectedValue(new ApiError('nope', status));
    const err: unknown = await followOperation(poll, { onTrouble, sleep: noWait }).catch((e) => e);
    expect(isOperationLostError(err)).toBe(true);
    expect(err).toBeInstanceOf(OperationLostError);
    expect((err as OperationLostError).reason).toBe(status === 404 || status === 410 ? 'gone' : 'auth');
    expect(poll).toHaveBeenCalledTimes(1);
    expect(onTrouble).not.toHaveBeenCalled();
  });

  it('a bounce to a login page is terminal too', async () => {
    const poll = vi.fn().mockRejectedValue(new ApiAuthRedirectError('/x', '/login'));
    await expect(followOperation(poll, { sleep: noWait })).rejects.toBeInstanceOf(
      OperationLostError
    );
  });

  it.each([
    ['a timeout', new ApiTimeoutError('/x', 30000)],
    ['a 503', new ApiError('down', 503)],
    ['a network error', new TypeError('Failed to fetch')],
  ])('%s is retryable: it keeps polling', async (_name, error) => {
    const poll = vi.fn().mockRejectedValueOnce(error).mockResolvedValue('completed');
    await expect(followOperation(poll, { sleep: noWait })).resolves.toBe('completed');
    expect(poll).toHaveBeenCalledTimes(2);
  });

  it('a gone operation after earlier trouble still ends the follow', async () => {
    const poll = vi
      .fn()
      .mockRejectedValueOnce(new TypeError('Failed to fetch'))
      .mockRejectedValue(new ApiError('gone', 404));
    await expect(followOperation(poll, { sleep: noWait })).rejects.toBeInstanceOf(
      OperationLostError
    );
    expect(poll).toHaveBeenCalledTimes(2);
  });

  it('words each failure class differently', () => {
    expect(classifyFollowError(new ApiTimeoutError('/x', 1))).toBe('timeout');
    expect(describeFollowTrouble(new ApiTimeoutError('/x', 1))).toBe(
      'the server did not answer in time'
    );
    expect(describeFollowTrouble(new ApiError('x', 502))).toBe('the server answered 502');
    expect(describeFollowTrouble(new TypeError('Failed to fetch'))).toBe(
      'the server could not be reached'
    );
    expect(describeFollowTrouble(new ApiError('x', 404))).toBe(
      'the server no longer knows the operation'
    );
    expect(describeFollowTrouble(new ApiError('x', 401))).toBe(
      'this session is not allowed to read it'
    );
  });
});
