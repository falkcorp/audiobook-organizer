// file: web/src/modules/books/layout/connection.ts
// version: 0.1.0
// guid: 5c2f8a47-1d63-4e90-b7a4-e9d1c3f60b28
// last-edited: 2026-11-21

import type { ConnectionFeed } from '../../../components/layout/TopBarFrame';
import { eventSourceManager } from '../../../services/eventSourceManager';

/**
 * The SSE connection's state as a feed for the top bar. Module-level so its
 * `subscribe` keeps one identity and the frame subscribes once. It listens
 * for status only; the events themselves go to other subscribers.
 */
export const eventSourceConnection: ConnectionFeed = {
  subscribe: (onState) =>
    eventSourceManager.subscribe(
      () => undefined,
      (status) => onState(status.state)
    ),
};
