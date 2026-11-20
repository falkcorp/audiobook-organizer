// file: core/web/packages/shell/test/realtime.leader.test.ts
// version: 0.1.0
// guid: eb63a30b-4927-4c78-aa5d-3b1a7da17bc1
// last-edited: 2026-11-20

// 07 §7.9.2 / 05-realtime §4.8 item 7: one stream per browser per app. Tabs
// elect a leader (Web Locks, else a BroadcastChannel heartbeat), the leader
// fans frames out over the channel, and with neither API each visible tab
// holds its own stream.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { createRealtimeClient } from '../src/realtime';
import {
  closedFrame,
  FakeBroadcastNetwork,
  FakeFetch,
  FakeLockManager,
  FakePage,
  frame,
  recordingListener,
  resetFrame,
} from '../src/realtime/testing-fakes';

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
});
afterEach(() => {
  vi.useRealTimers();
});

const settle = () => vi.advanceTimersByTimeAsync(0);
const advance = (ms: number) => vi.advanceTimersByTimeAsync(ms);

const GADGETS = 'batcave.gadgets.v1';
const COWL = 'batcave.cowl.v1';
const LOCK = 'batcave.realtime';

interface Env {
  locks?: FakeLockManager;
  net?: FakeBroadcastNetwork;
}

/** One browser tab: its own fetch (so calls are attributable), listener and page events. */
function tab(tabId: string, env: Env) {
  const server = new FakeFetch();
  const rec = recordingListener();
  const page = new FakePage();
  const client = createRealtimeClient({
    appId: 'batcave',
    tabId,
    fetch: server.fetch,
    locks: env.locks?.asLockManager() ?? null,
    channel: env.net ? env.net.open : () => null,
    win: page,
    doc: page,
    random: () => 1,
  });
  client.subscribe(rec.listener);
  return { tabId, server, rec, page, client, stream: () => server.calls.at(-1)!.stream! };
}

describe('one stream per browser: Web Locks', () => {
  it('only the leader opens a fetch, and the follower hears events through the channel', async () => {
    const env: Env = { locks: new FakeLockManager(), net: new FakeBroadcastNetwork() };
    const a = tab('wayne-1', env);
    const b = tab('wayne-2', env);
    a.client.start();
    b.client.start();
    await settle();

    expect(a.server.calls).toHaveLength(1);
    expect(b.server.calls).toHaveLength(0);
    expect(env.locks!.isHeld(LOCK)).toBe(true);
    expect(env.locks!.pending(LOCK)).toBe(1);

    // Each tab filters for itself: the follower's set does not limit what the leader hears.
    b.client.setTopics([COWL]);
    a.stream().push(frame(GADGETS, 1, { id: 'wayne-1' }) + frame(COWL, 2, { id: 'wayne-2' }));
    await settle();
    expect(a.rec.events.map((e) => e.data)).toEqual([1, 2]);
    expect(b.rec.events.map((e) => e.data)).toEqual([2]);
  });

  it('reset frames, closes and transport errors reach the follower too', async () => {
    const env: Env = { locks: new FakeLockManager(), net: new FakeBroadcastNetwork() };
    const a = tab('wayne-1', env);
    const b = tab('wayne-2', env);
    a.client.start();
    b.client.start();
    await settle();

    a.stream().push(resetFrame('overflow') + closedFrame('shutdown'));
    await settle();
    expect(b.rec.resets).toEqual(['overflow']);
    expect(b.rec.closed).toEqual(['shutdown']);

    a.stream().fail();
    await settle();
    expect(b.rec.errors).toEqual([undefined]);
    expect(a.rec.errors).toEqual([undefined]);
    expect(b.server.calls).toHaveLength(0); // the leader retries; the follower stays quiet
  });

  it('when the leader stops, the follower takes over from the last event it was relayed', async () => {
    const env: Env = { locks: new FakeLockManager(), net: new FakeBroadcastNetwork() };
    const a = tab('wayne-1', env);
    const b = tab('wayne-2', env);
    a.client.start();
    b.client.start();
    await settle();
    a.stream().push(frame(GADGETS, 1, { id: 'wayne-1' }) + frame(GADGETS, 2, { id: 'wayne-2' }));
    await settle();

    a.client.stop();
    await settle();
    expect(a.server.calls[0].aborted).toBe(true);
    expect(b.server.calls).toHaveLength(1);
    expect(b.server.calls[0].lastEventId).toBe('wayne-2');
    expect(b.rec.resets).toEqual([]); // the server can replay from there
    expect(env.locks!.pending(LOCK)).toBe(0);
    expect(env.net!.size).toBe(1);

    b.stream().push(frame(GADGETS, 3, { id: 'wayne-3' }));
    await settle();
    expect(b.rec.events.map((e) => e.data)).toEqual([1, 2, 3]);
  });

  it('a takeover with no resume point refetches, and every tab hears it', async () => {
    const env: Env = { locks: new FakeLockManager(), net: new FakeBroadcastNetwork() };
    const a = tab('wayne-1', env);
    const b = tab('wayne-2', env);
    const c = tab('wayne-3', env);
    for (const t of [a, b, c]) t.client.start();
    await settle();
    a.stream().push(frame('core.stream.opened.v1', { topics: ['*'] })); // live, but no event carried an id
    await settle();

    a.client.stop();
    await settle();
    expect(b.server.calls).toHaveLength(1);
    expect(b.server.calls[0].lastEventId).toBeNull();
    expect(b.rec.resets).toEqual(['gap']);
    expect(c.rec.resets).toEqual(['gap']);
    expect(c.server.calls).toHaveLength(0);
  });

  it('an event the new leader is replayed is not delivered twice', async () => {
    const env: Env = { locks: new FakeLockManager(), net: new FakeBroadcastNetwork() };
    const a = tab('wayne-1', env);
    const b = tab('wayne-2', env);
    const c = tab('wayne-3', env);
    for (const t of [a, b, c]) t.client.start();
    await settle();
    a.stream().push(frame(GADGETS, 1, { id: 'wayne-1' }) + frame(GADGETS, 2, { id: 'wayne-2' }));
    await settle();
    a.client.stop();
    await settle();

    // The server replays from wayne-2 inclusive (a lagging relay), then goes live.
    b.stream().push(frame(GADGETS, 2, { id: 'wayne-2' }) + frame(GADGETS, 3, { id: 'wayne-3' }));
    await settle();
    expect(b.rec.events.map((e) => e.data)).toEqual([1, 2, 3]);
    expect(c.rec.events.map((e) => e.data)).toEqual([1, 2, 3]);
  });

  it.each(['pagehide', 'freeze'])('a leader releases on %s, and refetches when the page comes back', async (type) => {
    const env: Env = { locks: new FakeLockManager(), net: new FakeBroadcastNetwork() };
    const a = tab('wayne-1', env);
    const b = tab('wayne-2', env);
    a.client.start();
    b.client.start();
    await settle();
    a.stream().push(frame(GADGETS, 1, { id: 'wayne-1' }));
    await settle();

    a.page.fire(type);
    await settle();
    expect(a.server.calls[0].aborted).toBe(true);
    expect(b.server.calls).toHaveLength(1);
    expect(b.server.calls[0].lastEventId).toBe('wayne-1');

    // Back from the freeze: it rejoins as a follower, and it missed frames while out.
    a.page.fire(type === 'pagehide' ? 'pageshow' : 'resume');
    await settle();
    expect(a.rec.resets).toEqual(['gap']);
    expect(a.server.calls).toHaveLength(1);
    expect(env.locks!.pending(LOCK)).toBe(1);
    b.stream().push(frame(GADGETS, 2, { id: 'wayne-2' }));
    await settle();
    expect(a.rec.events.map((e) => e.data)).toEqual([1, 2]);
  });

  it('a revoked credential retires every tab: no follower takes the stream over', async () => {
    const env: Env = { locks: new FakeLockManager(), net: new FakeBroadcastNetwork() };
    const a = tab('wayne-1', env);
    const b = tab('wayne-2', env);
    a.client.start();
    b.client.start();
    await settle();

    a.stream().push(closedFrame('credential_revoked'));
    await settle();
    expect(a.rec.closed).toEqual(['credential_revoked']);
    expect(b.rec.closed).toEqual(['credential_revoked']);
    await advance(10 * 60_000);
    expect(a.server.calls).toHaveLength(1);
    expect(b.server.calls).toHaveLength(0);
    expect(env.locks!.isHeld(LOCK)).toBe(false);
    expect(env.locks!.pending(LOCK)).toBe(0);

    // A new sign-in in any tab starts afresh.
    b.client.stop();
    b.client.start();
    await settle();
    expect(b.server.calls).toHaveLength(1);
  });

  it('stop() releases the lock, the channel, the page listeners and every timer', async () => {
    const env: Env = { locks: new FakeLockManager(), net: new FakeBroadcastNetwork() };
    const a = tab('wayne-1', env);
    const b = tab('wayne-2', env);
    a.client.start();
    b.client.start();
    await settle();
    expect(a.page.listenerCount()).toBeGreaterThan(0);

    b.client.stop();
    a.client.stop();
    await settle();
    expect(env.locks!.isHeld(LOCK)).toBe(false);
    expect(env.locks!.pending(LOCK)).toBe(0);
    expect(env.net!.size).toBe(0);
    expect(a.page.listenerCount() + b.page.listenerCount()).toBe(0);
    expect(a.server.open).toHaveLength(0);
    expect(vi.getTimerCount()).toBe(0);
  });
});

describe('one stream per browser: BroadcastChannel heartbeat (no Web Locks)', () => {
  it('only the leader opens a fetch; a newcomer does not take a healthy leader over, whatever its ID', async () => {
    const env: Env = { net: new FakeBroadcastNetwork() };
    const a = tab('wayne-2', env);
    a.client.start();
    await settle();
    expect(a.server.calls).toHaveLength(0); // listening for an incumbent first
    await advance(250);
    expect(a.server.calls).toHaveLength(1);

    const b = tab('wayne-1', env); // the lower ID, arriving late
    b.client.start();
    await advance(60_000);
    expect(a.server.calls).toHaveLength(1);
    expect(a.server.calls[0].aborted).toBe(false);
    expect(b.server.calls).toHaveLength(0);

    a.stream().push(frame(GADGETS, 1, { id: 'wayne-1' }));
    await settle();
    expect(b.rec.events.map((e) => e.data)).toEqual([1]);
  });

  it('the lowest tab ID wins a tie', async () => {
    const env: Env = { net: new FakeBroadcastNetwork() };
    env.net!.hold = true; // both claims are in flight before either is heard
    const high = tab('wayne-2', env);
    const low = tab('wayne-1', env);
    high.client.start();
    low.client.start();
    await advance(250);
    expect(high.server.calls).toHaveLength(1);
    expect(low.server.calls).toHaveLength(1);

    env.net!.release();
    await settle();
    expect(high.server.calls[0].aborted).toBe(true);
    expect(low.server.calls[0].aborted).toBe(false);

    await advance(60_000);
    expect(high.server.calls).toHaveLength(1); // it does not claim again while the winner beats
    expect(low.server.calls).toHaveLength(1);
    low.stream().push(frame(GADGETS, 1, { id: 'wayne-1' }));
    await settle();
    expect(high.rec.events.map((e) => e.data)).toEqual([1]);
    expect(low.rec.events.map((e) => e.data)).toEqual([1]);
  });

  it('when the leader stops, the follower takes over at once, resuming from the last event', async () => {
    const env: Env = { net: new FakeBroadcastNetwork() };
    const a = tab('wayne-1', env);
    const b = tab('wayne-2', env);
    a.client.start();
    await advance(250);
    b.client.start();
    await settle();
    a.stream().push(frame(GADGETS, 1, { id: 'wayne-1' }));
    await settle();

    a.client.stop();
    await settle(); // no timers advanced: the resignation, not a timeout, hands over
    expect(a.server.calls[0].aborted).toBe(true);
    expect(b.server.calls).toHaveLength(1);
    expect(b.server.calls[0].lastEventId).toBe('wayne-1');
    expect(b.rec.resets).toEqual([]);
  });

  it('a follower takes over after 3 missed beats, not before', async () => {
    const env: Env = { net: new FakeBroadcastNetwork() };
    const a = tab('wayne-1', env);
    const b = tab('wayne-2', env);
    a.client.start();
    await advance(250); // a claims and beats at 250
    b.client.start();
    await advance(2000); // a beats again at 2250
    expect(b.server.calls).toHaveLength(0);

    env.net!.endpoints[0].muted = true; // a hangs: its beats stop arriving
    await advance(5999);
    expect(b.server.calls).toHaveLength(0);
    await advance(1);
    expect(b.server.calls).toHaveLength(1);
  });

  it('a leader releases on pagehide, so the follower does not wait for the missed beats', async () => {
    const env: Env = { net: new FakeBroadcastNetwork() };
    const a = tab('wayne-1', env);
    const b = tab('wayne-2', env);
    a.client.start();
    await advance(250);
    b.client.start();
    await settle();

    a.page.fire('pagehide');
    await settle();
    expect(a.server.calls[0].aborted).toBe(true);
    expect(b.server.calls).toHaveLength(1);

    a.page.fire('pageshow'); // back as a follower of b, which is the leader now
    await advance(60_000);
    expect(a.server.calls).toHaveLength(1);
    expect(b.server.calls).toHaveLength(1);
    expect(a.rec.resets).toEqual(['gap']);
  });

  it('stop() clears the heartbeat, the watchdog, the channel and the page listeners', async () => {
    const env: Env = { net: new FakeBroadcastNetwork() };
    const a = tab('wayne-1', env);
    const b = tab('wayne-2', env);
    a.client.start();
    await advance(250);
    b.client.start();
    await settle();
    expect(vi.getTimerCount()).toBeGreaterThan(0);

    a.client.stop();
    b.client.stop();
    await settle();
    expect(vi.getTimerCount()).toBe(0);
    expect(env.net!.size).toBe(0);
    expect(a.page.listenerCount() + b.page.listenerCount()).toBe(0);
    expect(a.server.open.length + b.server.open.length).toBe(0);
  });
});

describe('last resort: neither Web Locks nor BroadcastChannel', () => {
  it('each visible tab opens its own stream', async () => {
    const a = tab('wayne-1', {});
    const b = tab('wayne-2', {});
    a.client.start();
    b.client.start();
    await settle();
    expect(a.server.calls).toHaveLength(1);
    expect(b.server.calls).toHaveLength(1);
    a.stream().push(frame(GADGETS, 1, { id: 'wayne-1' }));
    await settle();
    expect(a.rec.events).toHaveLength(1);
    expect(b.rec.events).toHaveLength(0); // no fan-out without a channel
  });

  it('Web Locks without a BroadcastChannel cannot fan out, so it falls back to a stream per tab', async () => {
    const env: Env = { locks: new FakeLockManager() };
    const a = tab('wayne-1', env);
    const b = tab('wayne-2', env);
    a.client.start();
    b.client.start();
    await settle();
    expect(a.server.calls).toHaveLength(1);
    expect(b.server.calls).toHaveLength(1);
    expect(env.locks!.isHeld(LOCK)).toBe(false);
  });

  it('a hidden tab opens nothing, and opens its stream when it is shown', async () => {
    const a = tab('wayne-1', {});
    a.page.visibilityState = 'hidden';
    a.client.start();
    await settle();
    expect(a.server.calls).toHaveLength(0);

    a.page.setVisibility('visible');
    await settle();
    expect(a.server.calls).toHaveLength(1);
    expect(a.rec.resets).toEqual([]); // the first stream of the tab: nothing was missed
  });

  it('closes the stream when the tab is hidden, and reopens it and refetches when shown again', async () => {
    const a = tab('wayne-1', {});
    a.client.start();
    await settle();
    a.stream().push(frame(GADGETS, 1, { id: 'wayne-1' }));
    await settle();

    a.page.setVisibility('hidden');
    await settle();
    expect(a.server.calls[0].aborted).toBe(true);
    await advance(60_000);
    expect(a.server.calls).toHaveLength(1);

    a.page.setVisibility('visible');
    await settle();
    expect(a.server.calls).toHaveLength(2);
    expect(a.server.calls[1].lastEventId).toBe('wayne-1');
    expect(a.rec.resets).toEqual(['gap']);
  });

  it('closes the stream on pagehide and reopens it on pageshow', async () => {
    const a = tab('wayne-1', {});
    a.client.start();
    await settle();
    a.page.fire('pagehide');
    expect(a.server.calls[0].aborted).toBe(true);
    a.page.fire('pageshow');
    await settle();
    expect(a.server.calls).toHaveLength(2);
    expect(a.rec.resets).toEqual(['gap']);
  });

  it('a revoked credential keeps a shown tab from reopening', async () => {
    const a = tab('wayne-1', {});
    a.client.start();
    await settle();
    a.stream().push(closedFrame('credential_revoked'));
    await settle();
    a.page.setVisibility('hidden');
    a.page.setVisibility('visible');
    await advance(60_000);
    expect(a.server.calls).toHaveLength(1);
  });

  it('stop() removes the page listeners and aborts the stream', async () => {
    const a = tab('wayne-1', {});
    a.client.start();
    await settle();
    a.client.stop();
    expect(a.server.open).toHaveLength(0);
    expect(a.page.listenerCount()).toBe(0);
    a.page.setVisibility('hidden');
    a.page.setVisibility('visible');
    await settle();
    expect(a.server.calls).toHaveLength(1);
    expect(vi.getTimerCount()).toBe(0);
  });
});
