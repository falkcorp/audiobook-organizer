// file: web/src/test/queryWrapper.tsx
// version: 1.0.0
// guid: 9e3b7d40-1c6a-4f85-b2e9-6d0a4c18f273
// last-edited: 2026-10-17

import React from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';

/** A fresh client per test: no retries, so a failure shows on the first try. */
export function createTestQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
}

/** A renderHook/render wrapper; pass a client to inspect its cache. */
export function createQueryWrapper(client = createTestQueryClient()) {
  return ({ children }: { children: React.ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
}
