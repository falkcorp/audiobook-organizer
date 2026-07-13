// file: web/src/test/setup.ts
// version: 1.0.6
// guid: 8f9a0b1c-2d3e-4f5a-6b7c-8d9e0f1a2b3c
// last-edited: 2026-07-13

import '@testing-library/jest-dom';
import { cleanup } from '@testing-library/react';
import { afterEach } from 'vitest';

const ResponseCtor = globalThis.Response;

// Cleanup after each test case
afterEach(() => {
  cleanup();
});

// Mock window.matchMedia
Object.defineProperty(window, 'matchMedia', {
  writable: true,
  value: (query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addListener: () => {},
    removeListener: () => {},
    addEventListener: () => {},
    removeEventListener: () => {},
    dispatchEvent: () => {},
  }),
});

// Mock IntersectionObserver
global.IntersectionObserver = class IntersectionObserver {
  readonly root: Element | null = null;
  readonly rootMargin: string = '';
  readonly thresholds: ReadonlyArray<number> = [];

  constructor() {}

  disconnect(): void {}
  observe(): void {}
  takeRecords(): IntersectionObserverEntry[] {
    return [];
  }
  unobserve(): void {}
} as unknown as typeof IntersectionObserver;

// Mock localStorage for jsdom environment
const storage = new Map<string, string>();
Object.defineProperty(global, 'localStorage', {
  value: {
    getItem: (key: string) => storage.get(key) ?? null,
    setItem: (key: string, value: string) => {
      storage.set(key, value);
    },
    removeItem: (key: string) => {
      storage.delete(key);
    },
    clear: () => {
      storage.clear();
    },
  },
});

// Mock EventSource (SSE) for tests. Mirrors the native readyState numbering
// (0 CONNECTING / 1 OPEN / 2 CLOSED) and exposes the same static constants,
// since store code (useOperationsStore's openSSE) branches on
// `es.readyState === EventSource.CLOSED` to distinguish a transient,
// auto-reconnecting drop from a terminal one.
class MockEventSource {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 2;

  url: string;
  onmessage: ((event: MessageEvent) => void) | null = null;
  onerror: ((event: Event) => void) | null = null;
  readyState = MockEventSource.OPEN;

  constructor(url: string) {
    this.url = url;
  }

  addEventListener() {}
  removeEventListener() {}
  close() {
    this.readyState = MockEventSource.CLOSED;
  }
}

// @ts-expect-error - EventSource is not properly typed in global namespace
global.EventSource = MockEventSource;

// Mock fetch to avoid network calls in tests
const okJson = (data: unknown) => {
  if (ResponseCtor) {
    return Promise.resolve(
      new ResponseCtor(JSON.stringify(data), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    );
  }

  // Fallback for environments without Response
  return Promise.resolve({
    json: () => Promise.resolve(data),
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  } as unknown as Response);
};

global.fetch = (input: Parameters<typeof fetch>[0]) => {
  const url = typeof input === 'string' ? input : input.toString();

  if (url.includes('/api/v1/system/status')) {
    return okJson({
      status: 'ok',
      library: { book_count: 0, folder_count: 1, total_size: 0 },
      import_paths: { book_count: 0, folder_count: 0, total_size: 0 },
      memory: {},
      runtime: {},
      operations: { recent: [] },
    });
  }

  if (url.includes('/api/v1/import-paths')) {
    return okJson({ importPaths: [] });
  }

  // Default empty response
  return okJson({});
};
