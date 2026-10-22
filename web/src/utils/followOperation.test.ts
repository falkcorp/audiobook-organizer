// file: web/src/utils/followOperation.test.ts
// version: 1.0.0
// guid: 8e1b4c27-6a39-4d50-9f82-0c5d7a3b1e64
// last-edited: 2026-10-22

import { describe, expect, it, vi } from 'vitest';
import { followOperation } from './followOperation';

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
