// file: web/src/components/StaleBuildBanner.test.tsx
// version: 1.0.0
// guid: b7122aae-38ff-485c-8454-4bfbcb71c306
// last-edited: 2026-11-08

import { afterEach, describe, expect, it, vi } from 'vitest';
import { act, render, screen } from '@testing-library/react';

// The page's build ID is read when apiFetch loads, so each case sets the meta
// and imports fresh copies of apiFetch and the banner together (they must
// share the one module instance).
async function mount(pageId: string | null) {
  document.head.querySelectorAll('meta[name="mediacore-build"]').forEach((m) => m.remove());
  if (pageId !== null) {
    const meta = document.createElement('meta');
    meta.setAttribute('name', 'mediacore-build');
    meta.setAttribute('content', pageId);
    document.head.appendChild(meta);
  }
  vi.resetModules();
  const { apiFetch } = await import('../utils/apiFetch');
  const { StaleBuildBanner } = await import('./StaleBuildBanner');
  return { apiFetch, StaleBuildBanner };
}

function stubServerBuild(id: string) {
  vi.stubGlobal(
    'fetch',
    vi.fn(
      async () =>
        new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json', 'X-App-Build': id } })
    )
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
  document.head.querySelectorAll('meta[name="mediacore-build"]').forEach((m) => m.remove());
});

describe('StaleBuildBanner', () => {
  it('is hidden when the server answers the page\'s own build', async () => {
    const { apiFetch, StaleBuildBanner } = await mount('same-1');
    render(<StaleBuildBanner />);
    stubServerBuild('same-1');
    await act(async () => {
      await apiFetch('/api/v1/health');
    });
    expect(screen.queryByText(/new version is available/i)).not.toBeInTheDocument();
  });

  it('is hidden when the page ID is dev, whatever the server says', async () => {
    const { apiFetch, StaleBuildBanner } = await mount('dev');
    render(<StaleBuildBanner />);
    stubServerBuild('new-2');
    await act(async () => {
      await apiFetch('/api/v1/health');
    });
    expect(screen.queryByText(/new version is available/i)).not.toBeInTheDocument();
  });

  it('is hidden when the page has no build ID', async () => {
    const { apiFetch, StaleBuildBanner } = await mount(null);
    render(<StaleBuildBanner />);
    stubServerBuild('new-2');
    await act(async () => {
      await apiFetch('/api/v1/health');
    });
    expect(screen.queryByText(/new version is available/i)).not.toBeInTheDocument();
  });

  it('is shown on a mismatch, with a Reload button that reloads the page', async () => {
    const { apiFetch, StaleBuildBanner } = await mount('old-1');
    render(<StaleBuildBanner />);
    stubServerBuild('new-2');
    await act(async () => {
      await apiFetch('/api/v1/health');
    });
    expect(await screen.findByText(/new version is available/i)).toBeInTheDocument();

    const reload = vi.fn();
    const original = window.location;
    Object.defineProperty(window, 'location', { configurable: true, value: { ...original, reload } });
    try {
      screen.getByRole('button', { name: 'Reload' }).click();
      expect(reload).toHaveBeenCalledTimes(1);
    } finally {
      Object.defineProperty(window, 'location', { configurable: true, value: original });
    }
  });

  it('is shown when it mounts after the response that proved the build stale', async () => {
    const { apiFetch, StaleBuildBanner } = await mount('old-1');
    stubServerBuild('new-2');
    await apiFetch('/api/v1/health');
    render(<StaleBuildBanner />);
    expect(await screen.findByText(/new version is available/i)).toBeInTheDocument();
  });
});
