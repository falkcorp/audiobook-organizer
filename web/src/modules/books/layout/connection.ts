// file: web/src/modules/books/layout/connection.ts
// version: 0.1.0
// guid: 5c2f8a47-1d63-4e90-b7a4-e9d1c3f60b28
// last-edited: 2026-11-21

import type { ConnectionFeed, ConnectionState } from '../../../components/layout/TopBarFrame';
import { getEventStream, type StreamStatus } from '../../../services/coreEvents';

/**
 * The top bar's connection state for a core stream status, or null for the states
 * that say nothing about the link: idle (nobody needs the stream), hidden (the tab is
 * in the background and the stream is closed on purpose) and connecting (the first
 * attempt, which has no banner of its own).
 */
export function connectionStateOf(status: StreamStatus): ConnectionState | null {
  if (status.state === 'open') return 'open';
  if (status.state === 'reconnecting') return 'reconnecting';
  return null;
}

/**
 * The shared core event stream's state as a feed for the top bar. Module-level so its
 * `subscribe` keeps one identity and the frame subscribes once. It listens for the
 * shared stream's status only (a keyed stream's is another view's), and it holds
 * nothing open: the shutdown watcher in App and the Library's topic subscription do.
 * A bar that mounts while the stream is already reconnecting is told so at once.
 */
export const coreEventsConnection: ConnectionFeed = {
  subscribe: (onState) => {
    const stream = getEventStream();
    const push = (status: StreamStatus) => {
      const state = connectionStateOf(status);
      if (state) onState(state);
    };
    push(stream.getStatus());
    return stream.onStatus((status) => {
      if (status.streamKey === '') push(status);
    });
  },
};
