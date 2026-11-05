// file: web/src/hooks/useUrlState.test.tsx
// version: 1.0.0
// guid: 0c5e92d7-48ab-4f36-a1d0-6be7f3092c58
// last-edited: 2026-11-05

import { useState } from 'react';
import { act, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { createMemoryRouter, RouterProvider, useNavigate } from 'react-router-dom';
import { describe, expect, it, vi } from 'vitest';
import { applyOwnedParams, parseEnum, useUrlEnumParam, useUrlStateSync } from './useUrlState';

const TABS = ['info', 'storage', 'quota'] as const;

function TabsProbe() {
  const [tab, setTab] = useUrlEnumParam('tab', TABS, 'info');
  return (
    <div>
      <div data-testid="tab">{tab}</div>
      {TABS.map((t) => (
        <button key={t} onClick={() => setTab(t)}>
          go-{t}
        </button>
      ))}
    </div>
  );
}

function mountRouter(element: React.ReactNode, initial: string) {
  const router = createMemoryRouter(
    [
      { path: '/page', element },
      { path: '/other', element: <div>other</div> },
    ],
    { initialEntries: [initial] }
  );
  render(<RouterProvider router={router} />);
  return router;
}

describe('applyOwnedParams', () => {
  it('sets owned keys, drops empty and default ones, and keeps foreign keys', () => {
    const next = applyOwnedParams(
      new URLSearchParams('keep=1&tab=old&gone=x'),
      { tab: 'storage', gone: null, page: '1', blank: '' },
      { page: '1' }
    );
    expect(next.toString()).toBe('keep=1&tab=storage');
  });
});

describe('parseEnum', () => {
  it('falls back for an unknown or missing value', () => {
    expect(parseEnum('quota', TABS, 'info')).toBe('quota');
    expect(parseEnum('nope', TABS, 'info')).toBe('info');
    expect(parseEnum(null, TABS, 'info')).toBe('info');
  });
});

describe('useUrlEnumParam', () => {
  it('seeds from the URL and an unknown value reads as the default', () => {
    mountRouter(<TabsProbe />, '/page?tab=quota');
    expect(screen.getByTestId('tab').textContent).toBe('quota');
  });

  it('reads the default when the param is absent or invalid', () => {
    mountRouter(<TabsProbe />, '/page?tab=bogus');
    expect(screen.getByTestId('tab').textContent).toBe('info');
  });

  it('writes the URL with replace, keeps other keys, and omits the default', async () => {
    const user = userEvent.setup();
    const router = mountRouter(<TabsProbe />, '/page?keep=1');
    await user.click(screen.getByText('go-storage'));
    expect(router.state.location.search).toBe('?keep=1&tab=storage');
    expect(router.state.historyAction).toBe('REPLACE');
    await user.click(screen.getByText('go-info'));
    expect(router.state.location.search).toBe('?keep=1');
  });

  it('follows back/forward (a navigation it did not make)', async () => {
    const router = mountRouter(<TabsProbe />, '/page');
    await act(async () => {
      await router.navigate('/page?tab=quota');
    });
    expect(screen.getByTestId('tab').textContent).toBe('quota');
    await act(async () => {
      await router.navigate(-1);
    });
    expect(screen.getByTestId('tab').textContent).toBe('info');
  });
});

function SyncProbe({
  onExternal,
  renderSpy,
}: {
  onExternal: (p: URLSearchParams) => void;
  renderSpy: () => void;
}) {
  const [lane, setLane] = useState('metadata');
  const [fixer, setFixer] = useState<string | null>(null);
  const navigate = useNavigate();
  renderSpy();
  useUrlStateSync(
    { lane, fixer: lane === 'repairs' ? fixer : null },
    {
      defaults: { lane: 'metadata' },
      onExternalChange: (p) => {
        onExternal(p);
        setLane(p.get('lane') ?? 'metadata');
        setFixer(p.get('fixer'));
      },
    }
  );
  return (
    <div>
      <div data-testid="lane">{lane}</div>
      <button onClick={() => setLane('repairs')}>repairs</button>
      <button onClick={() => setLane('metadata')}>metadata</button>
      <button onClick={() => setFixer('batcave-cleanup')}>fixer</button>
      <button onClick={() => navigate('/page?lane=repairs&fixer=joker-ledger')}>link</button>
    </div>
  );
}

describe('useUrlStateSync', () => {
  it('does not rewrite the URL it arrived on', () => {
    const router = mountRouter(
      <SyncProbe onExternal={vi.fn()} renderSpy={vi.fn()} />,
      '/page?lane=repairs&fixer=riddler&extra=1'
    );
    expect(router.state.location.search).toBe('?lane=repairs&fixer=riddler&extra=1');
  });

  it('writes owned keys on change with replace, drops defaults and non-applicable keys', async () => {
    const user = userEvent.setup();
    const onExternal = vi.fn();
    const router = mountRouter(
      <SyncProbe onExternal={onExternal} renderSpy={vi.fn()} />,
      '/page?keep=1'
    );
    await user.click(screen.getByText('repairs'));
    expect(router.state.location.search).toBe('?keep=1&lane=repairs');
    expect(router.state.historyAction).toBe('REPLACE');
    await user.click(screen.getByText('fixer'));
    expect(router.state.location.search).toBe('?keep=1&lane=repairs&fixer=batcave-cleanup');
    await user.click(screen.getByText('metadata'));
    expect(router.state.location.search).toBe('?keep=1');
    // None of our own writes came back as an external change.
    expect(onExternal).not.toHaveBeenCalled();
  });

  it('renders once per state change: the mirror adds no render of its own state', async () => {
    const user = userEvent.setup();
    const renderSpy = vi.fn();
    mountRouter(<SyncProbe onExternal={vi.fn()} renderSpy={renderSpy} />, '/page');
    renderSpy.mockClear();
    await user.click(screen.getByText('repairs'));
    // One render for the state change, one for the router's URL echo. A feedback
    // loop would render without bound or call onExternal.
    expect(renderSpy.mock.calls.length).toBeLessThanOrEqual(2);
  });

  it('re-seeds state from an external URL change, then ignores its own echo', async () => {
    const user = userEvent.setup();
    const onExternal = vi.fn();
    const router = mountRouter(<SyncProbe onExternal={onExternal} renderSpy={vi.fn()} />, '/page');
    await user.click(screen.getByText('link'));
    expect(onExternal).toHaveBeenCalledTimes(1);
    expect(screen.getByTestId('lane').textContent).toBe('repairs');
    expect(router.state.location.search).toBe('?lane=repairs&fixer=joker-ledger');
  });

  it('re-seeds on back/forward', async () => {
    const user = userEvent.setup();
    const onExternal = vi.fn();
    const router = mountRouter(<SyncProbe onExternal={onExternal} renderSpy={vi.fn()} />, '/page');
    await user.click(screen.getByText('link'));
    expect(screen.getByTestId('lane').textContent).toBe('repairs');
    await act(async () => {
      await router.navigate(-1);
    });
    expect(screen.getByTestId('lane').textContent).toBe('metadata');
    expect(onExternal).toHaveBeenCalledTimes(2);
  });

  it('treats a later navigation to a URL we once wrote as external, not as an echo', async () => {
    const user = userEvent.setup();
    const onExternal = vi.fn();
    const router = mountRouter(<SyncProbe onExternal={onExternal} renderSpy={vi.fn()} />, '/page');
    await user.click(screen.getByText('repairs')); // we write ?lane=repairs
    await act(async () => {
      await router.navigate('/page?lane=metadata'); // external
    });
    await act(async () => {
      await router.navigate('/page?lane=repairs'); // same string we wrote earlier
    });
    expect(onExternal).toHaveBeenCalledTimes(2);
    expect(screen.getByTestId('lane').textContent).toBe('repairs');
  });
});
