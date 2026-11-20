// file: core/web/packages/shell/src/realtime/index.ts
// version: 0.1.0
// guid: 744d088b-412a-4c8a-9502-4a8e04166fa7
// last-edited: 2026-11-20

export { createRealtimeClient } from './client';
export type { DocumentTarget, PageTarget, RealtimeClientOptions } from './client';
export { RealtimeProvider, useResync, useTopic } from './react';
export type { RealtimeProviderProps } from './react';
