// file: web/src/services/coreEvents/bookChanges.test.ts
// version: 0.1.0
// guid: ce4010a5-8277-457c-96ca-835e4bc7b5a2
// last-edited: 2026-11-20

import { act, renderHook } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import type { BooksChangedEvent } from '../../hooks/useLibraryQuery';
import { createFakeServer } from '../../test/eventStreamFake';
import {
  BOOK_CHANGED_TOPIC,
  BOOK_CHANGE_WINDOW_MS,
  createBookChangeBatcher,
  parseBookChange,
  subscribeBookChanges,
  useBookChanges,
  type BookChangeBatch,
} from './bookChanges';
import { createEventStream, type EventStream } from './manager';
import { TOPIC_RESET } from './types';

const tick = (ms = 0) => vi.advanceTimersByTimeAsync(ms);

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

function collect() {
  const batches: BookChangeBatch[] = [];
  return { batches, onBatch: (b: BookChangeBatch) => batches.push(b) };
}

describe('createBookChangeBatcher', () => {
  it('holds changes for one fixed window armed by the first, then delivers them as one batch', async () => {
    const { batches, onBatch } = collect();
    const b = createBookChangeBatcher(onBatch);
    b.add('updated', 'bk-year-one');
    await tick(100);
    b.add('updated', 'bk-long-halloween');
    await tick(BOOK_CHANGE_WINDOW_MS - 101);
    expect(batches).toEqual([]);
    await tick(1);
    expect(batches).toEqual([{ kind: 'updated', ids: ['bk-long-halloween', 'bk-year-one'] }]);
  });

  it('does not starve: a change every 50 ms still flushes every window', async () => {
    const { batches, onBatch } = collect();
    const b = createBookChangeBatcher(onBatch);
    for (let i = 0; i < 12; i++) {
      b.add('updated', `bk-${String(i).padStart(2, '0')}`);
      await tick(50);
    }
    expect(batches.length).toBeGreaterThanOrEqual(2);
    expect(batches[0].ids).toEqual(['bk-00', 'bk-01', 'bk-02', 'bk-03', 'bk-04']);
    b.dispose();
  });

  it('deleted supersedes created and updated in the same window', async () => {
    const { batches, onBatch } = collect();
    const b = createBookChangeBatcher(onBatch);
    b.add('created', 'bk-hush');
    b.add('updated', 'bk-knightfall');
    b.add('deleted', 'bk-hush');
    b.add('deleted', 'bk-knightfall');
    b.flush();
    expect(batches).toEqual([{ kind: 'deleted', ids: ['bk-hush', 'bk-knightfall'] }]);
  });

  it('drops an update for an id already created or deleted in the window', () => {
    const { batches, onBatch } = collect();
    const b = createBookChangeBatcher(onBatch);
    b.add('created', 'bk-court-of-owls');
    b.add('updated', 'bk-court-of-owls');
    b.add('deleted', 'bk-death-in-the-family');
    b.add('updated', 'bk-death-in-the-family');
    b.flush();
    expect(batches).toEqual([
      { kind: 'deleted', ids: ['bk-death-in-the-family'] },
      { kind: 'created', ids: ['bk-court-of-owls'] },
    ]);
  });

  it('flushes in the order deleted, created, updated, each sorted, and dedups ids', () => {
    const { batches, onBatch } = collect();
    const b = createBookChangeBatcher(onBatch);
    b.add('updated', 'bk-z');
    b.add('created', 'bk-y');
    b.add('deleted', 'bk-x');
    b.add('updated', 'bk-a');
    b.add('updated', 'bk-a');
    b.flush();
    expect(batches).toEqual([
      { kind: 'deleted', ids: ['bk-x'] },
      { kind: 'created', ids: ['bk-y'] },
      { kind: 'updated', ids: ['bk-a', 'bk-z'] },
    ]);
  });

  it('splits a large window into batches of at most maxIds', () => {
    const { batches, onBatch } = collect();
    const b = createBookChangeBatcher(onBatch, { maxIds: 2 });
    for (const id of ['bk-1', 'bk-2', 'bk-3', 'bk-4', 'bk-5']) b.add('updated', id);
    b.flush();
    expect(batches.map((x) => x.ids)).toEqual([['bk-1', 'bk-2'], ['bk-3', 'bk-4'], ['bk-5']]);
  });

  it('a throwing handler does not stop the remaining batches', () => {
    const errors: unknown[] = [];
    const seen: string[] = [];
    const b = createBookChangeBatcher(
      (x) => {
        seen.push(x.kind);
        if (x.kind === 'deleted') throw new Error('boom');
      },
      { onError: (e) => errors.push(e) },
    );
    b.add('deleted', 'bk-1');
    b.add('updated', 'bk-2');
    b.flush();
    expect(seen).toEqual(['deleted', 'updated']);
    expect(errors).toHaveLength(1);
  });

  it('clear drops what is pending and its timer; the next change opens a new window', async () => {
    const { batches, onBatch } = collect();
    const b = createBookChangeBatcher(onBatch);
    b.add('created', 'bk-1');
    b.clear();
    expect(vi.getTimerCount()).toBe(0);
    await tick(BOOK_CHANGE_WINDOW_MS);
    expect(batches).toEqual([]);
    b.add('created', 'bk-2');
    await tick(BOOK_CHANGE_WINDOW_MS);
    expect(batches).toEqual([{ kind: 'created', ids: ['bk-2'] }]);
  });

  it('dispose leaves no timer and ignores later changes', async () => {
    const { batches, onBatch } = collect();
    const b = createBookChangeBatcher(onBatch);
    b.add('updated', 'bk-1');
    b.dispose();
    b.add('updated', 'bk-2');
    expect(vi.getTimerCount()).toBe(0);
    await tick(BOOK_CHANGE_WINDOW_MS);
    expect(batches).toEqual([]);
  });

  it('a batch is the shape applyBooksChanged takes', () => {
    const batch: BookChangeBatch = { kind: 'deleted', ids: ['bk-1'] };
    const legacy: BooksChangedEvent = batch;
    const back: BookChangeBatch = legacy;
    expect(back).toBe(batch);
  });
});

describe('parseBookChange', () => {
  it('accepts {kind, id} for each kind and rejects anything else', () => {
    expect(parseBookChange({ kind: 'created', id: 'bk-1' })).toEqual({ kind: 'created', id: 'bk-1' });
    expect(parseBookChange({ kind: 'updated', id: 'bk-1', extra: 1 })).toEqual({ kind: 'updated', id: 'bk-1' });
    expect(parseBookChange({ kind: 'deleted', id: 'bk-1' })).toEqual({ kind: 'deleted', id: 'bk-1' });
    for (const bad of [null, 'x', 7, {}, { kind: 'moved', id: 'bk-1' }, { kind: 'updated' }, { kind: 'updated', id: '' }, { kind: 'updated', id: 9 }]) {
      expect(parseBookChange(bad)).toBeNull();
    }
  });
});

describe('subscribeBookChanges', () => {
  let es: EventStream;
  afterEach(() => es?.dispose());

  function make() {
    const server = createFakeServer();
    es = createEventStream({
      visibility: { visible: true, onChange: () => () => {} },
      deps: { fetch: server.fetch, random: () => 0 },
    });
    return server;
  }

  it('folds per-book frames from the shared stream into batches', async () => {
    const server = make();
    const s = server.openStream();
    const { batches, onBatch } = collect();
    subscribeBookChanges(es, onBatch);
    await tick();
    s.event('n-1', BOOK_CHANGED_TOPIC, { kind: 'updated', id: 'bk-killing-joke' }, 'bk-killing-joke');
    s.event('n-2', BOOK_CHANGED_TOPIC, { kind: 'updated', id: 'bk-dark-victory' }, 'bk-dark-victory');
    s.event('n-3', BOOK_CHANGED_TOPIC, { kind: 'deleted', id: 'bk-killing-joke' }, 'bk-killing-joke');
    await tick(BOOK_CHANGE_WINDOW_MS);
    expect(server.calls[0].url).toBe('/api/core/v1/events?topic=*&exclude=core.task.log-appended.v1');
    expect(batches).toEqual([
      { kind: 'deleted', ids: ['bk-killing-joke'] },
      { kind: 'updated', ids: ['bk-dark-victory'] },
    ]);
  });

  it('reports a malformed frame instead of dropping it silently', async () => {
    const server = make();
    const s = server.openStream();
    const invalid: string[] = [];
    const { batches, onBatch } = collect();
    subscribeBookChanges(es, onBatch, { onInvalid: (e) => invalid.push(e.id) });
    await tick();
    s.event('n-1', BOOK_CHANGED_TOPIC, { kind: 'moved', id: 'bk-1' }, 'bk-1');
    await tick(BOOK_CHANGE_WINDOW_MS);
    expect(invalid).toEqual(['n-1']);
    expect(batches).toEqual([]);
  });

  it('a shared-stream resync drops what is pending', async () => {
    const server = make();
    const s = server.openStream();
    const { batches, onBatch } = collect();
    subscribeBookChanges(es, onBatch);
    await tick();
    s.event('n-1', BOOK_CHANGED_TOPIC, { kind: 'created', id: 'bk-1' }, 'bk-1');
    await tick(10);
    s.control(TOPIC_RESET, { reason: 'overflow' });
    await tick(BOOK_CHANGE_WINDOW_MS);
    expect(batches).toEqual([]);
  });

  it('the unsubscribe removes the topic and resync listeners, drops the pending window and closes the stream', async () => {
    const server = make();
    const s = server.openStream();
    const { batches, onBatch } = collect();
    const off = subscribeBookChanges(es, onBatch);
    await tick();
    s.event('n-1', BOOK_CHANGED_TOPIC, { kind: 'updated', id: 'bk-1' }, 'bk-1');
    await tick(10);
    off();
    off(); // idempotent
    await tick(BOOK_CHANGE_WINDOW_MS);
    expect(batches).toEqual([]);
    expect(server.calls[0].init.signal!.aborted).toBe(true);
  });

  it('counts its registrations to zero on unsubscribe', () => {
    const live = { topics: 0, resync: 0 };
    const fake = {
      subscribe: () => {
        live.topics++;
        return () => void live.topics--;
      },
      onResync: () => {
        live.resync++;
        return () => void live.resync--;
      },
    } as unknown as EventStream;
    const off = subscribeBookChanges(fake, () => {});
    expect(live).toEqual({ topics: 1, resync: 1 });
    off();
    off();
    expect(live).toEqual({ topics: 0, resync: 0 });
  });
});

describe('useBookChanges', () => {
  let es: EventStream;
  afterEach(() => es?.dispose());

  it('delivers to the latest handler while mounted and stops on unmount', async () => {
    const server = createFakeServer();
    es = createEventStream({
      visibility: { visible: true, onChange: () => () => {} },
      deps: { fetch: server.fetch, random: () => 0 },
    });
    const s = server.openStream();
    const seen: string[] = [];
    const { rerender, unmount } = renderHook(({ tag }) => useBookChanges((b) => seen.push(`${tag}:${b.ids.join()}`), { source: es }), {
      initialProps: { tag: 'a' },
    });
    await act(() => tick());
    rerender({ tag: 'b' });
    s.event('n-1', BOOK_CHANGED_TOPIC, { kind: 'updated', id: 'bk-1' }, 'bk-1');
    await act(() => tick(BOOK_CHANGE_WINDOW_MS));
    expect(seen).toEqual(['b:bk-1']);
    expect(server.calls).toHaveLength(1);
    unmount();
    await act(() => tick());
    expect(server.calls[0].init.signal!.aborted).toBe(true);
  });
});
