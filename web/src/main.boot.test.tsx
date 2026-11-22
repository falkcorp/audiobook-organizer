// file: web/src/main.boot.test.tsx
// version: 0.1.0
// guid: 3e9a5c17-8d24-4f60-b1a7-c5d2e0f8a936
// last-edited: 2026-11-21

// The booted SPA (main.tsx itself, not a test harness) has a shell app ID, so
// the shell's saved state works. Before it did, storageKey() threw inside the
// operations bell, the throw was swallowed, and the collapse state was never
// saved in the running app.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, screen, within } from '@testing-library/react';

// main.tsx creates its own React root, which neither Testing Library's cleanup nor
// emptying document.body unmounts. A root left mounted keeps work alive past the test:
// the real AuthProvider's status check retries three times a second apart after the
// stubbed fetch rejects, and when it finally calls setState the file may already be
// over and jsdom gone, so React reads window.event on a missing window ("window is not
// defined", an unhandled error that fails the run although every test passed). Every
// root main creates is recorded here and unmounted when its page "unloads".
const roots = vi.hoisted(() => [] as { unmount: () => void }[]);

vi.mock('react-dom/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react-dom/client')>();
  const createRoot: typeof actual.createRoot = (...args) => {
    const root = actual.createRoot(...args);
    roots.push(root);
    return root;
  };
  return { ...actual, createRoot, default: { ...actual, createRoot } };
});

/** What a page unload does to the booted app: unmount its root, so its effects clean up. */
function unloadPage() {
  act(() => {
    for (const root of roots.splice(0)) root.unmount();
  });
}

vi.mock('./App', async () => {
  const { OperationsIndicator } = await import('./components/layout/OperationsIndicator');
  return { default: () => <OperationsIndicator /> };
});

const KEY = 'abo.operations.indicatorCollapse';
const done = { id: 'c1', type: 'scan', status: 'completed', progress: 1, total: 1, message: '' };

async function boot() {
  unloadPage(); // a fresh page load replaces the previous page's React root
  vi.resetModules();
  document.body.innerHTML = '<div id="root"></div>';
  await import('./main');
  await act(async () => {});
  // vi.resetModules gave main a fresh store module too; seed the one it renders from.
  const { useOperationsStore: store } = await import('./stores/useOperationsStore');
  await act(async () => {
    store.setState({
      operations: { c1: done as never },
      activeOperations: [],
      alertOperations: [done as never],
      groupedOperations: [done as never],
      loadError: null,
    });
  });
}

beforeEach(() => {
  localStorage.clear();
  vi.stubGlobal('fetch', vi.fn(() => Promise.reject(new Error('no network in this test'))));
});
afterEach(() => {
  unloadPage();
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

describe('main.tsx boot', () => {
  it('sets the shell app ID before the first render', async () => {
    await boot();
    const { storageKey: key } = await import('@mediacore/shell');
    expect(key('operations', 'indicatorCollapse')).toBe(KEY);
  });

  it('persists the bell collapse toggle across a remount', async () => {
    const toggleCompleted = () => {
      fireEvent.click(screen.getByTestId('operations-bell'));
      fireEvent.click(within(screen.getByRole('presentation')).getByText(/^Completed/));
    };
    const saved = () => JSON.parse(localStorage.getItem(KEY) ?? 'null');

    await boot();
    toggleCompleted(); // Completed starts collapsed, so this opens it
    expect(saved()).toMatchObject({ completed: true });

    await boot(); // a fresh page load: the state is read back, not reset to the default
    toggleCompleted(); // so this closes it again
    expect(saved()).toMatchObject({ completed: false });
  });
});
