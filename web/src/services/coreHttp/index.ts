// file: web/src/services/coreHttp/index.ts
// version: 0.1.0
// guid: 5e1c0f9b-2d6a-4b7e-8c33-0a4d9b6f17e2
// last-edited: 2026-11-20

// Public surface of the shared raw transport of the core clients (coreUpdater,
// coreBackups). UNWIRED: nothing in the running app imports it yet.

export {
  DEFAULT_TIMEOUT_MS,
  MAX_RETRY_AFTER_MS,
  createTransport,
  parseRetryAfterMs,
} from './transport';
export type { RawRequest, Refusal, Transport, TransportOptions } from './transport';
