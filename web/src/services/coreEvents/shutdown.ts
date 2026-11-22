// file: web/src/services/coreEvents/shutdown.ts
// version: 0.1.0
// guid: cb1721bf-2651-47dc-96e9-e97a4af4ce20
// last-edited: 2026-11-20

// Server-shutdown adapter (F6-03 E2). The legacy hub's `system.shutdown` frame
// (internal/server/server_lifecycle.go) drives App.tsx's backdrop, and backup restore
// relies on it (useBackupHandlers.ts: "The server tells every connected page it is going
// down"). Its successor is core.stream.closed.v1 with reason 'shutdown'
// (05-realtime 4.2). This watches for it:
//  - it holds the shared stream itself (EventStream.retain), so the backdrop works on a
//    page with no topic subscriber; onClosed alone opens nothing;
//  - it fires once per shutdown, though every open stream (shared and each keyed one)
//    gets its own closed frame, and re-arms when the shared stream opens again;
//  - the detach removes every listener it added and releases the stream.
//
// Wired by the F6-03 flip: App.tsx, TopBar and the Library use it.

import { useEffect, useLayoutEffect, useRef } from 'react';

import { getEventStream, type EventStream } from './manager';

export interface ServerShutdownInfo {
  /** The stream whose closed frame announced it ('' = the shared stream). */
  streamKey: string;
}

export type ServerShutdownListener = (info: ServerShutdownInfo) => void;

/** Call `listener` once when the server announces it is shutting down. Returns the detach. */
export function watchServerShutdown(stream: EventStream, listener: ServerShutdownListener): () => void {
  let fired = false;
  const release = stream.retain();
  const offClosed = stream.onClosed(({ reason, streamKey }) => {
    if (reason !== 'shutdown' || fired) return;
    fired = true;
    listener({ streamKey });
  });
  const offStatus = stream.onStatus((status) => {
    if (status.streamKey === '' && status.state === 'open') fired = false;
  });
  let done = false;
  return () => {
    if (done) return;
    done = true;
    offClosed();
    offStatus();
    release();
  };
}

/** Run `listener` when the server shuts down (show the backdrop); detaches on unmount. */
export function useServerShutdown(listener: ServerShutdownListener, source?: EventStream): void {
  const ref = useRef(listener);
  useLayoutEffect(() => {
    ref.current = listener;
  });
  useEffect(() => watchServerShutdown(source ?? getEventStream(), (i) => ref.current(i)), [source]);
}
