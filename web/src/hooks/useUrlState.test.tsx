// file: web/src/hooks/useUrlState.test.tsx
// version: 2.2.0
// guid: 0c5e92d7-48ab-4f36-a1d0-6be7f3092c58
// last-edited: 2026-11-05

import { lazy, StrictMode, Suspense, useState } from 'react';
import { act, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import {
  BrowserRouter,
  createMemoryRouter,
  Link,
  Route,
  RouterProvider,
  Routes,
  useNavigate,
  useSearchParams,
} from 'react-router-dom';
import { describe, expect, it, vi } from 'vitest';
import { FlushOnNavigate } from '../components/FlushOnNavigate';
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

// These run under BrowserRouter on purpose: it applies a location change in a
// transition, so what a page has rendered can trail the address bar, and the
// hook's decision to drop a late write depends on reading the live location.
describe('useUrlStateSync: a late write never undoes a navigation', () => {
  const here = () => window.location.pathname + window.location.search;
  const wait = (ms: number) =>
    act(async () => {
      await new Promise((r) => setTimeout(r, ms));
    });

  it('a trailing write does not fire after a link to a page that is still loading', async () => {
    window.history.replaceState(null, '', '/a');
    let resolveB: (m: { default: () => React.ReactElement }) => void = () => {};
    const PageB = lazy(
      () =>
        new Promise<{ default: () => React.ReactElement }>((r) => {
          resolveB = r;
        })
    );
    function PageA() {
      return (
        <div>
          <Counter />
          <Link to="/b?n=1">go-b</Link>
        </div>
      );
    }
    render(
      <BrowserRouter>
        <Suspense fallback={<p>loading</p>}>
          <Routes>
            <Route path="/a" element={<PageA />} />
            <Route path="/b" element={<PageB />} />
          </Routes>
        </Suspense>
      </BrowserRouter>
    );
    const user = userEvent.setup();
    await user.click(screen.getByRole('button'));
    await user.click(screen.getByRole('button'));
    // The second change is waiting on the throttle. Leave the page now.
    await user.click(screen.getByText('go-b'));
    // The link carries the same query as the rendered page, so only the pathname
    // tells the trailing write that this page is on its way out.
    expect(here()).toBe('/b?n=1');
    await wait(WRITE_THROTTLE_MS + 200);
    expect(here()).toBe('/b?n=1');
    await act(async () => {
      resolveB({ default: () => <p>page-b</p> });
      await new Promise((r) => setTimeout(r, 50));
    });
    expect(here()).toBe('/b?n=1');
    expect(screen.getByText('page-b')).toBeInTheDocument();
  });

  it('a trailing write does not overwrite an external navigation that is still in flight', async () => {
    window.history.replaceState(null, '', '/a');
    render(
      <BrowserRouter>
        <Counter />
      </BrowserRouter>
    );
    const user = userEvent.setup();
    await user.click(screen.getByRole('button'));
    await user.click(screen.getByRole('button'));
    expect(here()).toBe('/a?n=1');
    // The address moves (Back, a sidebar link) but the router has not rendered it.
    window.history.pushState(null, '', '/a?ext=1');
    await wait(WRITE_THROTTLE_MS + 200);
    expect(here()).toBe('/a?ext=1');
  });

  it('a write still waiting on the throttle is flushed when the page unmounts', async () => {
    window.history.replaceState(null, '', '/a');
    function Host() {
      const [show, setShow] = useState(true);
      return (
        <div>
          {show && <Counter />}
          <button onClick={() => setShow(false)}>hide</button>
        </div>
      );
    }
    render(
      <BrowserRouter>
        <Host />
      </BrowserRouter>
    );
    const user = userEvent.setup();
    await user.click(screen.getByText('inc0'));
    await user.click(screen.getByText('inc1'));
    expect(here()).toBe('/a?n=1');
    await user.click(screen.getByText('hide'));
    // No waiting: the trailing write went out as the page unmounted.
    expect(here()).toBe('/a?n=2');
  });

  it('a write that throws is forgotten, so the same URL arriving later is not taken for an echo', async () => {
    window.history.replaceState(null, '', '/a');
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
    const external = vi.fn();
    function FailProbe() {
      const [n, setN] = useState(0);
      const navigate = useNavigate();
      useUrlStateSync(
        { n: n === 0 ? null : String(n) },
        {
          onExternalChange: (p) => {
            external(p.toString());
            setN(Number(p.get('n') ?? 0));
          },
        }
      );
      return (
        <div>
          <button onClick={() => setN(1)}>one</button>
          <button onClick={() => navigate('/a?n=1')}>link-n1</button>
        </div>
      );
    }
    render(
      <BrowserRouter>
        <FailProbe />
      </BrowserRouter>
    );
    const replace = vi.spyOn(window.history, 'replaceState').mockImplementationOnce(() => {
      throw new DOMException('too many calls', 'SecurityError');
    });
    try {
      const user = userEvent.setup();
      await user.click(screen.getByText('one'));
      expect(warn).toHaveBeenCalled();
      expect(here()).toBe('/a');
      await user.click(screen.getByText('link-n1'));
      expect(here()).toBe('/a?n=1');
      await waitFor(() => expect(external).toHaveBeenCalledWith('n=1'));
    } finally {
      replace.mockRestore();
      warn.mockRestore();
    }
  });
});

// The throttle leaves up to WRITE_THROTTLE_MS in which the page's latest state is
// not in the address bar. Leaving the page inside that window used to strand the
// entry on the pre-burst query: the unmount flush runs after the address has
// moved, so the live-location guard (rightly) drops it, and a pushed-over entry
// cannot be rewritten. FlushOnNavigate writes it before the push.
describe('useUrlStateSync: flushing before a navigation', () => {
  const here = () => window.location.pathname + window.location.search;
  const wait = (ms: number) =>
    act(async () => {
      await new Promise((r) => setTimeout(r, ms));
    });

  function SearchPage() {
    const [q, setQ] = useState(() => new URLSearchParams(window.location.search).get('q') ?? '');
    const navigate = useNavigate();
    useUrlStateSync({ q: q || null }, { onExternalChange: (p) => setQ(p.get('q') ?? '') });
    return (
      <div>
        <button onClick={() => setQ('Year One')}>year-one</button>
        <button onClick={() => setQ('The Long Halloween')}>long-halloween</button>
        <button onClick={() => navigate(-1)}>navigate-back</button>
        <Link to="/c">go-c</Link>
        <Link to="/a?q=Hush">go-hush</Link>
        <span data-testid="q">{q}</span>
      </div>
    );
  }
  function GothamPage() {
    const navigate = useNavigate();
    return <button onClick={() => navigate(-1)}>back-from-c</button>;
  }
  function mountApp(initial: string, flush = true, basename?: string) {
    window.history.replaceState(null, '', initial);
    const routes = (
      <Suspense fallback={<p>loading</p>}>
        <Routes>
          <Route path="/a" element={<SearchPage />} />
          <Route path="/c" element={<GothamPage />} />
        </Routes>
      </Suspense>
    );
    return render(
      <BrowserRouter basename={basename}>
        {flush ? <FlushOnNavigate>{routes}</FlushOnNavigate> : routes}
      </BrowserRouter>
    );
  }
  // Two changes inside the throttle: the first writes at once, the second waits.
  async function burst(user: ReturnType<typeof userEvent.setup>) {
    await user.click(screen.getByText('year-one'));
    await user.click(screen.getByText('long-halloween'));
    expect(here()).toBe('/a?q=Year+One');
  }

  it('Back after a link taken inside the throttle lands on the latest state, not the pre-burst one', async () => {
    mountApp('/a');
    const user = userEvent.setup();
    await burst(user);
    await user.click(screen.getByText('go-c'));
    expect(here()).toBe('/c');
    await user.click(screen.getByText('back-from-c'));
    expect(here()).toBe('/a?q=The+Long+Halloween');
    await waitFor(() => expect(screen.getByTestId('q').textContent).toBe('The Long Halloween'));
  });

  it('the same holds for the browser history button after a link', async () => {
    mountApp('/a');
    const user = userEvent.setup();
    await burst(user);
    await user.click(screen.getByText('go-c'));
    await act(async () => {
      window.history.back();
      await new Promise((r) => setTimeout(r, 50));
    });
    expect(here()).toBe('/a?q=The+Long+Halloween');
  });

  it('a link to the same route flushes first, so Back returns to the latest state', async () => {
    mountApp('/a');
    const user = userEvent.setup();
    await burst(user);
    await user.click(screen.getByText('go-hush'));
    await waitFor(() => expect(screen.getByTestId('q').textContent).toBe('Hush'));
    expect(here()).toBe('/a?q=Hush');
    await act(async () => {
      window.history.back();
      await new Promise((r) => setTimeout(r, 50));
    });
    expect(here()).toBe('/a?q=The+Long+Halloween');
  });

  it('navigate(-1) from the page flushes the entry it leaves', async () => {
    window.history.replaceState(null, '', '/c');
    window.history.pushState(null, '', '/a');
    mountApp('/a');
    window.history.replaceState(null, '', '/a');
    const user = userEvent.setup();
    await burst(user);
    await user.click(screen.getByText('navigate-back'));
    await wait(50);
    // The page left is /a; going Forward must show where the user was.
    await act(async () => {
      window.history.forward();
      await new Promise((r) => setTimeout(r, 50));
    });
    expect(here()).toBe('/a?q=The+Long+Halloween');
  });

  it('flushes when the page is hidden (reload, tab close) inside the window', async () => {
    mountApp('/a');
    const user = userEvent.setup();
    await burst(user);
    window.dispatchEvent(new Event('pagehide'));
    expect(here()).toBe('/a?q=The+Long+Halloween');
  });

  it('still never lets a late write undo a navigation to a page that is loading', async () => {
    window.history.replaceState(null, '', '/a');
    let resolveB: (m: { default: () => React.ReactElement }) => void = () => {};
    const PageB = lazy(
      () =>
        new Promise<{ default: () => React.ReactElement }>((r) => {
          resolveB = r;
        })
    );
    function PageA() {
      return (
        <div>
          <SearchPage />
          <Link to="/b">go-b</Link>
        </div>
      );
    }
    render(
      <BrowserRouter>
        <FlushOnNavigate>
          <Suspense fallback={<p>loading</p>}>
            <Routes>
              <Route path="/a" element={<PageA />} />
              <Route path="/b" element={<PageB />} />
            </Routes>
          </Suspense>
        </FlushOnNavigate>
      </BrowserRouter>
    );
    const user = userEvent.setup();
    await burst(user);
    await user.click(screen.getByText('go-b'));
    await wait(WRITE_THROTTLE_MS + 200);
    expect(here()).toBe('/b');
    await act(async () => {
      resolveB({ default: () => <p>page-b</p> });
      await new Promise((r) => setTimeout(r, 50));
    });
    expect(here()).toBe('/b');
    await act(async () => {
      window.history.back();
      await new Promise((r) => setTimeout(r, 50));
    });
    expect(here()).toBe('/a?q=The+Long+Halloween');
  });

  it.each([false, true])(
    'writes under a router basename (flush wrapper %s): the history path includes it, the rendered one does not',
    async (flush) => {
      mountApp('/app/a', flush, '/app');
      const user = userEvent.setup();
      await burst2(user);
      await wait(WRITE_THROTTLE_MS + 200);
      expect(here()).toBe('/app/a?q=The+Long+Halloween');
    }
  );
  async function burst2(user: ReturnType<typeof userEvent.setup>) {
    await user.click(screen.getByText('year-one'));
    await user.click(screen.getByText('long-halloween'));
  }
});

describe('observe: a batch that writes the same query twice', () => {
  const here = () => window.location.pathname + window.location.search;

  function Folder() {
    const write = useUrlParamWriter();
    const [sp] = useSearchParams();
    return (
      <div>
        <button
          onClick={() => {
            write({ x: 'Robin' });
            write({ x: 'Batgirl' });
            write({ x: 'Robin' });
          }}
        >
          three
        </button>
        <button onClick={() => write({ y: 'Alfred' })}>write-y</button>
        <span data-testid="x">{sp.get('x')}</span>
      </div>
    );
  }

  it('leaves no stale entry for the live-location guard to trust', async () => {
    window.history.replaceState(null, '', '/a');
    render(
      <BrowserRouter>
        <Folder />
      </BrowserRouter>
    );
    const user = userEvent.setup();
    await user.click(screen.getByText('three'));
    await waitFor(() => expect(screen.getByTestId('x').textContent).toBe('Robin'));
    // The address moves (Back, a sidebar link) before the router renders it. It is
    // a query the page once wrote, but one that was superseded, not in flight.
    window.history.pushState(null, '', '/a?x=Batgirl');
    await user.click(screen.getByText('write-y'));
    expect(here()).toBe('/a?x=Batgirl');
  });
});
