// file: core/web/packages/shell/src/realtime/client.ts
// version: 0.1.0
// guid: e0d2996c-e460-4dbd-922e-11691cc42e62
// last-edited: 2026-11-20

// The shell's realtime client (07 §7.9.2): one SSE stream per browser per app,
// held by an elected tab and fanned out to the others over a BroadcastChannel.
//
// Three modes, picked at start():
//   locks      navigator.locks and BroadcastChannel exist: the lock holder is the
//              leader, the others wait in the lock queue.
//   heartbeat  BroadcastChannel only (plain HTTP has no Web Locks): the leader
//              announces every 2 s, followers take over after 3 missed beats,
//              the lowest tab ID wins a tie.
//   visible    no BroadcastChannel (or locks without a channel to fan out over):
//              no election; each visible tab holds its own stream.

import type { ApiAuthRedirectError } from '@mediacore/api-client';

import type { CloseReason, RealtimeEvent, RealtimeListener, RealtimeSource, ResetReason } from '../contracts';
import { StreamConnection } from './connection';

export const EVENTS_PATH = '/api/core/v1/events';
/** Left off the shared stream (FE2-21): the tab that shows a run's log fetches it itself. */
export const LOG_TOPIC = 'core.task.log-appended.v1';
export const HEARTBEAT_MS = 2_000;
export const MISSED_BEATS = 3;
/** How long a new tab listens for an incumbent leader before it claims leadership. */
export const DISCOVERY_MS = 250;
const SEEN_WINDOW = 512;

/** The slice of `window` the client listens to (`pagehide`, `pageshow`). */
export interface PageTarget {
  addEventListener(type: string, listener: () => void): void;
  removeEventListener(type: string, listener: () => void): void;
}
/** The slice of `document` the client listens to (`freeze`, `resume`, `visibilitychange`). */
export interface DocumentTarget extends PageTarget {
  readonly visibilityState: DocumentVisibilityState;
}

export interface RealtimeClientOptions {
  /** The app ID: names the lock and the BroadcastChannel, `<appId>.realtime`. */
  appId: string;
  /** Prefix for the events URL; default '' (same origin). */
  baseUrl?: string;
  fetch?: typeof fetch;
  /** The Web Locks manager; undefined uses `navigator.locks`, null means unavailable. */
  locks?: LockManager | null;
  /** Opens the channel; may return null when BroadcastChannel is unavailable. */
  channel?: (name: string) => BroadcastChannel | null;
  now?: () => number;
  /** Returns [0, 1); used for backoff jitter and the default tab ID. */
  random?: () => number;
  setTimeout?: (fn: () => void, ms: number) => unknown;
  clearTimeout?: (handle: unknown) => void;
  /** This tab's ID for the heartbeat election (lowest wins); default random. */
  tabId?: string;
  /** Receives `pagehide` and `pageshow`; undefined uses `window`, null none. */
  win?: PageTarget | null;
  /** Receives `freeze`, `resume` and `visibilitychange`; undefined uses `document`, null none. */
  doc?: DocumentTarget | null;
  /** Called for an SSO bounce (05-realtime §4.8 item 4); the shell reloads the page here. */
  onAuthRedirect?: (error: ApiAuthRedirectError) => void;
}

type Wire =
  | { t: 'event'; id?: string; event: RealtimeEvent }
  | { t: 'reset'; reason: ResetReason }
  | { t: 'closed'; reason: CloseReason }
  | { t: 'error'; status?: number }
  | { t: 'live' }
  | { t: 'hb' | 'who' | 'resign'; tab: string };

const RESET_REASONS: readonly string[] = ['restart', 'gap', 'overflow'];
const CLOSE_REASONS: readonly string[] = ['shutdown', 'credential_revoked', 'permission_revoked'];

function parseWire(raw: unknown): Wire | null {
  if (raw === null || typeof raw !== 'object') return null;
  const m = raw as Record<string, unknown>;
  switch (m.t) {
    case 'event': {
      const e = m.event as { topic?: unknown } | null | undefined;
      if (e === null || typeof e !== 'object' || typeof e.topic !== 'string') return null;
      if (m.id !== undefined && typeof m.id !== 'string') return null;
      return raw as Wire;
    }
    case 'reset':
      return typeof m.reason === 'string' && RESET_REASONS.includes(m.reason) ? (raw as Wire) : null;
    case 'closed':
      return typeof m.reason === 'string' && CLOSE_REASONS.includes(m.reason) ? (raw as Wire) : null;
    case 'live':
      return raw as Wire;
    case 'error':
      return m.status === undefined || typeof m.status === 'number' ? (raw as Wire) : null;
    case 'hb':
    case 'who':
    case 'resign':
      return typeof m.tab === 'string' ? (raw as Wire) : null;
    default:
      return null;
  }
}

type Mode = 'locks' | 'heartbeat' | 'visible';

/** A listener's own exception must not kill the stream or its sibling listeners. */
function isolate(fn: () => void): void {
  try {
    fn();
  } catch (err) {
    queueMicrotask(() => {
      throw err;
    });
  }
}

function defaultTabId(random: () => number): string {
  const part = () => Math.floor(random() * 0xffffffff).toString(16).padStart(8, '0');
  return `${part()}${part()}`;
}

export function createRealtimeClient(opts: RealtimeClientOptions): RealtimeSource {
  return new RealtimeClient(opts);
}

class RealtimeClient implements RealtimeSource {
  private readonly listeners = new Set<RealtimeListener>();
  private topics: ReadonlySet<string> | null = null;

  private readonly tabId: string;
  private readonly name: string;
  private readonly url: string;
  private readonly fetchFn: typeof fetch;
  private readonly locks: LockManager | null;
  private readonly openChannel: (name: string) => BroadcastChannel | null;
  private readonly now: () => number;
  private readonly random: () => number;
  private readonly setTimer: (fn: () => void, ms: number) => unknown;
  private readonly clearTimer: (handle: unknown) => void;
  private readonly win: PageTarget | null;
  private readonly doc: DocumentTarget | null;
  private readonly onAuthRedirect: ((error: ApiAuthRedirectError) => void) | undefined;

  private started = false;
  private mode: Mode = 'visible';
  private channel: BroadcastChannel | null = null;
  private conn: StreamConnection | null = null;
  /** In the election and allowed to hold a stream (false after pagehide/freeze or a revocation). */
  private joined = false;
  /** Set when this tab's stream ended on a revoked credential: stays out until stop()/start(). */
  private retired = false;
  private leader = false;
  private leaderId: string | null = null;
  private hbTimer: unknown = null;
  private watchdog: unknown = null;
  private discovery: unknown = null;
  private lockAbort: AbortController | null = null;
  private releaseLock: (() => void) | null = null;
  private pageCleanup: (() => void) | null = null;
  private readonly seen = new Set<string>();
  /** Another tab's stream has been seen: taking it over is a continuation, not a first connect. */
  private sawLeader = false;
  /** Frames may have been missed (this tab left the election, or its stream was closed while hidden). */
  private needsResync = false;

  constructor(opts: RealtimeClientOptions) {
    this.name = `${opts.appId}.realtime`;
    this.url = `${opts.baseUrl ?? ''}${EVENTS_PATH}?${new URLSearchParams([
      ['topic', '*'],
      ['exclude', LOG_TOPIC],
    ]).toString()}`;
    this.fetchFn = opts.fetch ?? ((input, init) => globalThis.fetch(input, init));
    this.locks =
      opts.locks !== undefined ? opts.locks : typeof navigator !== 'undefined' && navigator.locks ? navigator.locks : null;
    this.openChannel =
      opts.channel ?? ((name) => (typeof BroadcastChannel === 'undefined' ? null : new BroadcastChannel(name)));
    this.now = opts.now ?? (() => Date.now());
    this.random = opts.random ?? (() => Math.random());
    this.setTimer = opts.setTimeout ?? ((fn, ms) => globalThis.setTimeout(fn, ms));
    this.clearTimer = opts.clearTimeout ?? ((h) => globalThis.clearTimeout(h as ReturnType<typeof setTimeout>));
    this.win = opts.win !== undefined ? opts.win : typeof window !== 'undefined' ? window : null;
    this.doc = opts.doc !== undefined ? opts.doc : typeof document !== 'undefined' ? document : null;
    this.onAuthRedirect = opts.onAuthRedirect;
    this.tabId = opts.tabId ?? defaultTabId(this.random);
  }

  subscribe(listener: RealtimeListener): () => void {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  }

  /**
   * The topics this tab has subscribers for. The shared stream is always `*` minus the log topic
   * (FE2-21), so nothing is sent to the server: this is the client-side filter between the stream
   * and this tab's listeners. Frames are relayed to other tabs unfiltered, since each tab has its own set.
   */
  setTopics(topics: readonly string[]): void {
    this.topics = new Set(topics);
  }

  start(): void {
    if (this.started) return;
    this.started = true;
    this.retired = false;
    this.needsResync = false;
    this.sawLeader = false;
    this.leaderId = null;
    this.conn = new StreamConnection(
      {
        url: this.url,
        fetch: this.fetchFn,
        setTimeout: this.setTimer,
        clearTimeout: this.clearTimer,
        random: this.random,
        now: this.now,
      },
      {
        onEvent: (event, id) => {
          if (!this.fresh(id)) return;
          this.deliver(event);
          this.post({ t: 'event', ...(id === undefined ? {} : { id }), event });
        },
        onOpened: () => this.post({ t: 'live' }),
        onReset: (reason) => {
          this.emitReset(reason);
          this.post({ t: 'reset', reason });
        },
        onClosed: (reason) => {
          this.emitClosed(reason);
          this.post({ t: 'closed', reason });
          // The connection has already stopped for good; give leadership up so another tab is not starved.
          if (reason !== 'shutdown') this.retire();
        },
        onError: (status) => {
          this.emitError(status);
          this.post({ t: 'error', ...(status === undefined ? {} : { status }) });
        },
        onAuthRedirect: (error) => {
          this.onAuthRedirect?.(error);
          this.emitError(0);
          this.post({ t: 'error', status: 0 });
        },
      },
    );

    const channel = this.openChannel(this.name);
    if (channel !== null && this.locks !== null) this.mode = 'locks';
    else if (channel !== null) this.mode = 'heartbeat';
    else this.mode = 'visible';
    this.channel = channel;
    if (channel !== null) {
      channel.onmessage = (ev) => this.onWire(ev.data);
      channel.onmessageerror = () => this.emitError(undefined);
    }
    this.listenToPage();
    this.join();
  }

  stop(): void {
    if (!this.started) return;
    this.leave();
    this.clearTimers();
    this.pageCleanup?.();
    this.pageCleanup = null;
    if (this.channel !== null) {
      this.channel.onmessage = null;
      this.channel.onmessageerror = null;
      this.channel.close();
      this.channel = null;
    }
    this.conn?.stop();
    this.conn = null;
    this.started = false;
    this.retired = false;
    this.needsResync = false;
    this.leader = false;
    this.leaderId = null;
    this.seen.clear();
  }

  // Fan-out ----------------------------------------------------------------

  /** True the first time an event ID is seen; guards against two leaders streaming at once. */
  private fresh(id: string | undefined): boolean {
    if (id === undefined) return true;
    if (this.seen.has(id)) return false;
    this.seen.add(id);
    if (this.seen.size > SEEN_WINDOW) {
      const oldest = this.seen.values().next().value;
      if (oldest !== undefined) this.seen.delete(oldest);
    }
    return true;
  }

  private deliver(event: RealtimeEvent): void {
    if (this.topics !== null && !this.topics.has(event.topic)) return;
    for (const l of [...this.listeners]) isolate(() => l.onEvent(event));
  }
  private emitReset(reason: ResetReason): void {
    for (const l of [...this.listeners]) isolate(() => l.onReset(reason));
  }
  private emitClosed(reason: CloseReason): void {
    for (const l of [...this.listeners]) isolate(() => l.onClosed(reason));
  }
  private emitError(status: number | undefined): void {
    for (const l of [...this.listeners]) isolate(() => l.onError?.(status));
  }

  private post(message: Wire): void {
    this.channel?.postMessage(message);
  }

  private onWire(raw: unknown): void {
    const msg = parseWire(raw);
    if (msg === null) return;
    // A tab that left the election hears nothing, except that a retired tab (its credential was
    // revoked) still takes data from whichever tab streams after a re-login.
    const data = msg.t === 'event' || msg.t === 'reset' || msg.t === 'closed' || msg.t === 'error';
    if (!this.joined && !(this.retired && data)) return;
    if (msg.t !== 'who' && msg.t !== 'resign') this.sawLeader = true;
    switch (msg.t) {
      case 'event':
        if (!this.fresh(msg.id)) return;
        if (msg.id !== undefined && this.conn !== null) this.conn.lastEventId = msg.id;
        this.deliver(msg.event);
        return;
      case 'reset':
        if (!this.leader) this.emitReset(msg.reason);
        return;
      case 'closed':
        if (this.leader) return;
        this.emitClosed(msg.reason);
        // The shared credential is gone for every tab: none of them takes the stream over.
        if (msg.reason !== 'shutdown') this.retire();
        return;
      case 'error':
        if (!this.leader) this.emitError(msg.status);
        return;
      case 'live':
        return;
      case 'who':
        if (this.leader) this.beat();
        return;
      case 'hb':
        if (this.leader) {
          // Two leaders: the lowest tab ID keeps the stream.
          if (msg.tab < this.tabId) {
            this.stepDown();
            this.follow(msg.tab);
          } else if (msg.tab > this.tabId) {
            this.beat();
          }
          return;
        }
        this.follow(msg.tab);
        return;
      case 'resign':
        if (!this.leader && msg.tab === this.leaderId) {
          this.leaderId = null;
          this.clearWatchdog();
          this.claim();
        }
        return;
    }
  }

  // Election ---------------------------------------------------------------

  private join(): void {
    if (this.joined || this.retired || this.conn === null) return;
    this.joined = true;
    if (this.mode !== 'visible') this.resyncIfMissed();
    switch (this.mode) {
      case 'locks':
        this.requestLock();
        return;
      case 'heartbeat':
        this.post({ t: 'who', tab: this.tabId });
        this.discovery = this.setTimer(() => {
          this.discovery = null;
          if (this.leaderId === null) this.claim();
        }, DISCOVERY_MS);
        return;
      case 'visible':
        this.syncVisible();
        return;
    }
  }

  /** Releases leadership (and any claim to it) without leaving the page; join() re-enters. */
  private leave(): void {
    if (!this.joined) return;
    this.joined = false;
    this.needsResync = true;
    switch (this.mode) {
      case 'locks':
        this.releaseLock?.();
        this.lockAbort?.abort();
        this.lockAbort = null;
        this.stepDown();
        return;
      case 'heartbeat':
        if (this.leader) this.post({ t: 'resign', tab: this.tabId });
        this.stepDown();
        this.clearWatchdog();
        if (this.discovery !== null) this.clearTimer(this.discovery);
        this.discovery = null;
        this.leaderId = null;
        return;
      case 'visible':
        this.conn?.stop();
        return;
    }
  }

  /** Tells this tab's listeners to refetch if frames may have gone by while it was out. */
  private resyncIfMissed(): void {
    if (!this.needsResync) return;
    this.needsResync = false;
    this.emitReset('gap');
    // The refetch above covers a stream that cannot be resumed: do not ask for a second one.
    this.sawLeader = false;
    if (this.conn !== null) this.conn.continuing = false;
  }

  private retire(): void {
    this.retired = true;
    this.leave();
  }

  private requestLock(): void {
    const locks = this.locks;
    if (locks === null) return;
    const ac = new AbortController();
    this.lockAbort = ac;
    locks
      .request(this.name, { signal: ac.signal }, () => {
        if (this.lockAbort !== ac || !this.joined) return undefined; // stale: released at once
        this.becomeLeader();
        return new Promise<void>((resolve) => {
          this.releaseLock = () => {
            this.releaseLock = null;
            resolve();
          };
        });
      })
      .catch((err: unknown) => {
        if ((err as { name?: string } | null)?.name === 'AbortError') return;
        // Election is impossible: surface it and fall back to a stream of this tab's own.
        this.emitError(undefined);
        this.mode = 'visible';
        this.syncVisible();
      });
  }

  private claim(): void {
    if (this.leader || !this.joined) return;
    this.leaderId = this.tabId;
    this.becomeLeader();
  }

  private becomeLeader(): void {
    if (this.leader) return;
    this.leader = true;
    this.clearWatchdog();
    if (this.discovery !== null) this.clearTimer(this.discovery);
    this.discovery = null;
    // Taking over a stream another tab held: with no resume point the server cannot replay what
    // went by, so the first connect reports a reset (which every tab hears).
    if (this.conn !== null && this.sawLeader) this.conn.continuing = true;
    this.conn?.start();
    if (this.mode === 'heartbeat') this.beat();
  }

  private stepDown(): void {
    this.leader = false;
    this.conn?.stop();
    if (this.hbTimer !== null) this.clearTimer(this.hbTimer);
    this.hbTimer = null;
  }

  /** Announces leadership now and again after one beat. */
  private beat(): void {
    this.post({ t: 'hb', tab: this.tabId });
    if (this.hbTimer !== null) this.clearTimer(this.hbTimer);
    this.hbTimer = this.setTimer(() => {
      this.hbTimer = null;
      if (this.leader) this.beat();
    }, HEARTBEAT_MS);
  }

  /** Records `tab` as the leader to follow (the lowest ID wins) and re-arms the missed-beat watchdog. */
  private follow(tab: string): void {
    if (this.leaderId !== null && tab > this.leaderId) return;
    this.leaderId = tab;
    if (this.discovery !== null) this.clearTimer(this.discovery);
    this.discovery = null;
    this.clearWatchdog();
    this.watchdog = this.setTimer(() => {
      this.watchdog = null;
      this.leaderId = null;
      this.claim();
    }, HEARTBEAT_MS * MISSED_BEATS);
  }

  private clearWatchdog(): void {
    if (this.watchdog !== null) this.clearTimer(this.watchdog);
    this.watchdog = null;
  }

  private clearTimers(): void {
    this.clearWatchdog();
    for (const h of [this.hbTimer, this.discovery]) if (h !== null) this.clearTimer(h);
    this.hbTimer = null;
    this.discovery = null;
  }

  // Page lifecycle ---------------------------------------------------------

  /** Visible mode: hold a stream only while the tab is visible. */
  private syncVisible(): void {
    const visible = this.doc === null || this.doc.visibilityState !== 'hidden';
    if (this.joined && visible) {
      // 05-realtime §4.8 item 7: a tab shown again refetches, whatever the stream replays.
      this.resyncIfMissed();
      this.conn?.start();
    } else {
      if (this.conn?.active === true) this.needsResync = true;
      this.conn?.stop();
    }
  }

  private listenToPage(): void {
    // A leader releases on pagehide and freeze (a frozen page would starve the others) and comes
    // back on pageshow and resume.
    const release = () => this.leave();
    const rejoin = () => this.join();
    const visibility = () => {
      if (this.mode === 'visible') this.syncVisible();
    };
    const targets: [PageTarget | null, string, () => void][] = [
      [this.win, 'pagehide', release],
      [this.win, 'pageshow', rejoin],
      [this.doc, 'freeze', release],
      [this.doc, 'resume', rejoin],
      [this.doc, 'visibilitychange', visibility],
    ];
    for (const [target, type, fn] of targets) target?.addEventListener(type, fn);
    this.pageCleanup = () => {
      for (const [target, type, fn] of targets) target?.removeEventListener(type, fn);
    };
  }
}
