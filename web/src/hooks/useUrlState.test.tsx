// file: web/src/hooks/useUrlState.test.tsx
// version: 2.0.0
// guid: 0c5e92d7-48ab-4f36-a1d0-6be7f3092c58
// last-edited: 2026-11-05

import { StrictMode, useState } from 'react';
import { act, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { BrowserRouter, createMemoryRouter, RouterProvider, useNavigate } from 'react-router-dom';
import { describe, expect, it, vi } from 'vitest';
import {
  applyOwnedParams,
  parseEnum,
  useUrlEnumParam,
  useUrlParamWriter,
  useUrlStateSync,
  WRITE_THROTTLE_MS,
} from './useUrlState';

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

function mountRouter(element: React.ReactNode, initial: string, strict = false) {
  const router = createMemoryRouter(
    [
      { path: '/page', element },
      { path: '/other', element: <div>other</div> },
    ],
    { initialEntries: [initial] }
  );
  render(
    strict ? (
      <StrictMode>
        <RouterProvider router={router} />
      </StrictMode>
    ) : (
      <RouterProvider router={router} />
    )
  );
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
    // The second write inside the throttle window waits for its trailing edge.
    await user.click(screen.getByText('fixer'));
    await waitFor(() =>
      expect(router.state.location.search).toBe('?keep=1&lane=repairs&fixer=batcave-cleanup')
    );
    await user.click(screen.getByText('metadata'));
    await waitFor(() => expect(router.state.location.search).toBe('?keep=1'));
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

// A page that adopts only part of what a URL says: it knows two lanes and one
// fixer, and ignores anything else.
function PickyProbe() {
  const [lane, setLane] = useState('metadata');
  const [fixer, setFixer] = useState<string | null>(null);
  const navigate = useNavigate();
  useUrlStateSync(
    { lane, fixer: lane === 'repairs' ? fixer : null },
    {
      defaults: { lane: 'metadata' },
      onExternalChange: (p) => {
        const named = p.get('lane');
        // A URL that names no lane leaves the screen where it is.
        if (!named) return;
        if (named === 'metadata' || named === 'repairs') setLane(named);
        const f = p.get('fixer');
        if (f === 'known-fixer') setFixer(f);
      },
    }
  );
  return (
    <div>
      <div data-testid="lane">{lane}</div>
      <button onClick={() => setLane('repairs')}>to-repairs</button>
      <button onClick={() => navigate('/page')}>bare-link</button>
      <button onClick={() => navigate('/page?lane=repairs&fixer=nope')}>unknown-fixer</button>
      <button onClick={() => navigate('/page?lane=repairs&fixer=known-fixer')}>known-fixer</button>
      <button onClick={() => navigate('/page?lane=bogus&keep=1')}>unknown-lane</button>
    </div>
  );
}

describe('useUrlStateSync: the address bar and the screen never disagree', () => {
  it('a link that names no lane leaves the screen alone, and the URL is rewritten to say so', async () => {
    const user = userEvent.setup();
    const router = mountRouter(<PickyProbe />, '/page');
    await user.click(screen.getByText('to-repairs'));
    await waitFor(() => expect(router.state.location.search).toBe('?lane=repairs'));
    await user.click(screen.getByText('bare-link')); // the sidebar's plain /page
    expect(screen.getByTestId('lane').textContent).toBe('repairs');
    await waitFor(() => expect(router.state.location.search).toBe('?lane=repairs'));
  });

  it('an unknown value in a link is not left in the address bar', async () => {
    const user = userEvent.setup();
    const router = mountRouter(<PickyProbe />, '/page');
    await user.click(screen.getByText('unknown-fixer'));
    await waitFor(() => expect(router.state.location.search).toBe('?lane=repairs'));
    await user.click(screen.getByText('unknown-lane'));
    // The lane stays repairs; the foreign key survives; the bogus lane does not.
    await waitFor(() => expect(router.state.location.search).toBe('?lane=repairs&keep=1'));
  });

  it('a link the page fully adopts is left exactly as it was', async () => {
    const user = userEvent.setup();
    const router = mountRouter(<PickyProbe />, '/page');
    await user.click(screen.getByText('known-fixer'));
    await waitFor(() => expect(screen.getByTestId('lane').textContent).toBe('repairs'));
    expect(router.state.location.search).toBe('?lane=repairs&fixer=known-fixer');
  });
});

function Counter() {
  const [n, setN] = useState(0);
  useUrlStateSync({ n: n === 0 ? null : String(n) });
  return <button onClick={() => setN((v) => v + 1)}>inc{n}</button>;
}

function WriterProbe() {
  const write = useUrlParamWriter();
  const [layer, setLayer] = useState<string | null>(null);
  useUrlStateSync({ layer });
  return (
    <div>
      <button
        onClick={() => {
          write({ a: '1' });
          write({ b: '2' });
        }}
      >
        two-writes
      </button>
      <button
        onClick={() => {
          setLayer('llm');
          write({ band: 'HIGH' });
        }}
      >
        layer-and-band
      </button>
      <button onClick={() => write({ band: 'LOW' })}>band-low</button>
      <button onClick={() => setLayer('exact')}>layer-exact</button>
    </div>
  );
}

describe('useUrlStateSync: writing safely', () => {
  it('does not rewrite the URL it arrived on under StrictMode', () => {
    const router = mountRouter(<PickyProbe />, '/page?lane=bogus&extra=1', true);
    expect(router.state.location.search).toBe('?lane=bogus&extra=1');
  });

  it('merges successive writes made before the router has committed the first', async () => {
    const user = userEvent.setup();
    const router = mountRouter(<WriterProbe />, '/page?keep=1');
    await user.click(screen.getByText('two-writes'));
    await waitFor(() => expect(router.state.location.search).toBe('?keep=1&a=1&b=2'));
  });

  it("a child's write and the mirror's write in one event both land", async () => {
    const user = userEvent.setup();
    const router = mountRouter(<WriterProbe />, '/page');
    await user.click(screen.getByText('layer-and-band'));
    await waitFor(() => expect(router.state.location.search).toBe('?band=HIGH&layer=llm'));
    // A later child write must not revert the layer the mirror wrote.
    await user.click(screen.getByText('band-low'));
    await waitFor(() => expect(router.state.location.search).toBe('?band=LOW&layer=llm'));
    await user.click(screen.getByText('layer-exact'));
    await waitFor(() => expect(router.state.location.search).toBe('?band=LOW&layer=exact'));
  });

  it('writes the last value of a burst even when the intermediate ones were throttled', async () => {
    const user = userEvent.setup();
    const router = mountRouter(<Counter />, '/page');
    const navigate = vi.spyOn(router, 'navigate');
    for (let i = 0; i < 8; i++) {
      await user.click(screen.getByRole('button'));
    }
    // 8 changes, at most a leading and a trailing write so far.
    expect(navigate.mock.calls.length).toBeLessThanOrEqual(2);
    await waitFor(() => expect(router.state.location.search).toBe('?n=8'), {
      timeout: WRITE_THROTTLE_MS * 4,
    });
    expect(navigate.mock.calls.length).toBeLessThanOrEqual(3);
  });

  it('survives history.replaceState throwing (the browser rate limit)', async () => {
    const user = userEvent.setup();
    // The app runs under BrowserRouter, where the throw is synchronous.
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
    let replace: ReturnType<typeof vi.spyOn> | undefined;
    try {
      render(
        <BrowserRouter>
          <Counter />
        </BrowserRouter>
      );
      // BrowserRouter touches replaceState while it mounts; the limit bites later.
      replace = vi.spyOn(window.history, 'replaceState').mockImplementation(() => {
        throw new DOMException('too many calls', 'SecurityError');
      });
      await user.click(screen.getByRole('button'));
      expect(replace).toHaveBeenCalled();
      expect(warn).toHaveBeenCalled();
      expect(screen.getByRole('button').textContent).toBe('inc1');
    } finally {
      replace?.mockRestore();
      warn.mockRestore();
    }
  });
});
