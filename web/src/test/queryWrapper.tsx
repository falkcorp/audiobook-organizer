// file: web/src/test/queryWrapper.tsx
// version: 1.0.0
// guid: 8b3e1c64-2d7a-4f95-b0c8-41a6e9d2f703
// last-edited: 2026-10-18

import { useState, type ReactNode } from 'react';
import { QueryClientProvider } from '@tanstack/react-query';
import { createQueryClient } from '../lib/queryClient';

/** A render/renderHook `wrapper`: a fresh cache per render, and no retries. */
export function QueryWrapper({ children }: { children: ReactNode }) {
  const [client] = useState(() => createQueryClient(false));
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}
