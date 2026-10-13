// file: web/src/components/review/DupesLabelsView.test.tsx
// version: 1.2.0
// guid: 6d1e8b35-4a92-4c07-9f58-3b7a0c2e6d81
// last-edited: 2026-10-13
//
// The Labels / Suspicious rendering, driven through the real hook against a
// stubbed fetch. Ported from pages/__tests__/DedupLabels.test.tsx (abbreviated
// path, filter params, override, suspicious reasons + override) and extended to
// the four states, clickable counts, and the evidence panel.
//
// The band debounce is replaced with identity (see useDupesLabels.test.tsx); no
// assertion here waits on a timer.

import { useState } from 'react';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { ThemeProvider } from '@mui/material/styles';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

// Pass-through, or frozen at its last value while `held` (the real hook during
// its wait). A test releases it and re-renders; nothing waits on a timer.
const debounce = vi.hoisted(() => ({ held: false }));
vi.mock('../../hooks/useDebouncedSearch', async () => {
  const { useRef } = await import('react');
  return {
    useDebouncedSearch: (value: string) => {
      const settled = useRef('');
      if (!debounce.held) settled.current = value;
      return settled.current;
    },
  };
});

import { appTheme } from '../../theme';
import { __resetPathVarsCacheForTests } from '../../utils/formatPath';
import { DupesLabelsView } from './DupesLabelsView';
import {
  useDupesLabels,
  type DupesSubView,
  type LabeledExample,
  type SuspiciousExample,
} from './lanes/useDupesLabels';

const LIBROOT = '/lib/root';

function json(body: unknown, status = 200) {
  return Promise.resolve({
    ok: status < 400,
    status,
    json: () => Promise.resolve(body),
  } as Response);
}

const sampleRow: LabeledExample = {
  candidate_id: 42,
  entity_a_id: 'BOOKA',
  entity_b_id: 'BOOKB',
  layer: 'exact',
  band: 'title_author',
  score: 71.4,
  score_breakdown: {
    score: 71.4,
    band: 'HIGH',
    formula: 'v3',
    signals: [
      {
        kind: 'exact_file',
        raw: 1,
        confidence: 0.99,
        evidence: 'identical file hash',
      },
    ],
  },
  label: 'unsure',
  label_source: 'rule',
  label_reason: 'part_vs_whole',
  a: { title: 'Book One', primary_path: `${LIBROOT}/Author A/Book One/01.m4b` },
  b: { title: 'Book One (CD)', primary_path: `${LIBROOT}/Author A/Book One CD/01.m4b` },
};

const suspiciousRow: SuspiciousExample = {
  candidate_id: 77,
  entity_a_id: 'SUSA',
  entity_b_id: 'SUSB',
  layer: 'embedding',
  band: 'CERTAIN',
  label: 'not_dup',
  label_source: 'rule',
  label_reason: 'part_vs_whole',
  suspicion_reasons: ['duplicate-likely band (CERTAIN)', 'cosine similarity >= 0.95'],
  a: {
    title: 'Book Two',
    primary_path: `${LIBROOT}/Author B/Book Two/01.m4b`,
    total_duration_sec: 3000,
  },
  b: {
    title: 'Book Two',
    primary_path: `${LIBROOT}/Author B/Book Two dup/01.m4b`,
    total_duration_sec: 3010,
  },
};

const STATS = {
  total: 5,
  by_label: { true_dup: 2, unsure: 1, unlabeled: 2 },
  by_source: { rule: 1, human: 2, itunes_attr: 2 },
};

let fetchMock: ReturnType<typeof vi.fn>;
let respond: {
  labels: () => Promise<Response>;
  suspicious: () => Promise<Response>;
  stats: () => Promise<Response>;
};

const listUrls = () =>
  fetchMock.mock.calls.map(([u]) => String(u)).filter((u) => u.includes('/dedup/labels?'));

beforeEach(() => {
  localStorage.clear();
  __resetPathVarsCacheForTests();
  respond = {
    labels: () => json({ data: { labels: [sampleRow], total: 1 } }),
    suspicious: () => json({ data: { labels: [suspiciousRow], total: 1 } }),
    stats: () => json({ data: STATS }),
  };
  fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    if (url.includes('/config')) return json({ data: { config: { root_dir: LIBROOT } } });
    if (url.includes('/override'))
      return json({ data: { status: 'updated', label: 'not_dup', label_source: 'human' } });
    if (url.includes('/dedup/labels/stats')) return respond.stats();
    if (url.includes('/dedup/labels/suspicious')) return respond.suspicious();
    if (url.includes('/dedup/labels')) return respond.labels();
    return json({ data: {} });
  });
  vi.stubGlobal('fetch', fetchMock);
});

afterEach(() => {
  cleanup();
  debounce.held = false;
  vi.unstubAllGlobals();
  vi.clearAllMocks();
});

// What DupesPanel does: it owns the sub-view, calls the hook, renders the view.
function Harness({ initial }: { initial: Exclude<DupesSubView, 'queue'> }) {
  const [view, setView] = useState<DupesSubView>(initial);
  const [tick, setTick] = useState(0);
  const labels = useDupesLabels(view);
  return (
    <>
      <span data-testid="current-view">{view}</span>
      <button type="button" onClick={() => setTick((t) => t + 1)}>
        rerender
      </button>
      <span data-testid="tick">{tick}</span>
      {view !== 'queue' && <DupesLabelsView view={view} labels={labels} onSelectView={setView} />}
    </>
  );
}

function renderView(initial: Exclude<DupesSubView, 'queue'> = 'labels') {
  return render(
    <MemoryRouter>
      <ThemeProvider theme={appTheme} defaultMode="dark">
        <Harness initial={initial} />
      </ThemeProvider>
    </MemoryRouter>
  );
}

describe('DupesLabelsView: Labels', () => {
  it('renders the abbreviated library path for a labeled pair', async () => {
    renderView();
    expect(await screen.findByText('$(libroot)/Author A/Book One/01.m4b')).toBeInTheDocument();
  });

  it('shows a loading indicator, then the rows', async () => {
    renderView();
    expect(screen.getByLabelText('Loading labels')).toBeInTheDocument();
    expect(await screen.findByText('Book One')).toBeInTheDocument();
    expect(screen.queryByLabelText('Loading labels')).not.toBeInTheDocument();
  });

  it('says so when no labeled example matches', async () => {
    respond.labels = () => json({ data: { labels: [], total: 0 } });
    renderView();
    expect(await screen.findByText('No labeled examples for this filter.')).toBeInTheDocument();
  });

  it('shows the error with a Retry, not the empty message', async () => {
    respond.labels = () => json({}, 500);
    renderView();
    expect(await screen.findByText('HTTP 500')).toBeInTheDocument();
    expect(screen.queryByText('No labeled examples for this filter.')).not.toBeInTheDocument();

    respond.labels = () => json({ data: { labels: [sampleRow], total: 1 } });
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
    expect(await screen.findByText('Book One')).toBeInTheDocument();
  });

  it('sends label, source, and band filters as query params', async () => {
    renderView();
    await screen.findByText('Book One');

    fireEvent.mouseDown(screen.getByLabelText('Label'));
    fireEvent.click(screen.getByRole('option', { name: 'true_dup' }));
    fireEvent.mouseDown(screen.getByLabelText('Source'));
    fireEvent.click(screen.getByRole('option', { name: 'human (gold)' }));
    fireEvent.change(screen.getByLabelText('Band'), { target: { value: 'title_author' } });

    await waitFor(() => {
      const url = listUrls().at(-1) ?? '';
      expect(url).toContain('label=true_dup');
      expect(url).toContain('label_source=human');
      expect(url).toContain('band=title_author');
    });
  });

  it('posts an override when a label toggle is clicked and shows the human source', async () => {
    renderView();
    await screen.findByText('Book One');
    fireEvent.click(screen.getByRole('button', { name: /^not$/i }));

    await waitFor(() => {
      const call = fetchMock.mock.calls.find(([u]) =>
        String(u).includes('/dedup/labels/42/override')
      );
      expect(call).toBeTruthy();
      expect(JSON.parse((call![1] as RequestInit).body as string)).toMatchObject({
        label: 'not_dup',
      });
    });
    expect(await screen.findByText('human')).toBeInTheDocument();
  });

  it('shows a failed override instead of swallowing it', async () => {
    fetchMock.mockImplementation((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes('/override')) return json({}, 500);
      if (url.includes('/dedup/labels/stats')) return respond.stats();
      if (url.includes('/dedup/labels')) return respond.labels();
      return json({ data: { config: { root_dir: LIBROOT } } });
    });
    renderView();
    await screen.findByText('Book One');
    fireEvent.click(screen.getByRole('button', { name: /^not$/i }));
    expect(await screen.findByText('HTTP 500')).toBeInTheDocument();
  });
});

describe('DupesLabelsView: stats counts', () => {
  it('renders every count as a button', async () => {
    renderView();
    const strip = await screen.findByTestId('dupes-labels-stats');
    for (const name of [
      'total 5',
      'true_dup: 2',
      'unsure: 1',
      'unlabeled: 2',
      'rule: 1',
      'human: 2',
    ]) {
      expect(within(strip).getByRole('button', { name })).toBeInTheDocument();
    }
  });

  it('a label count filters the table to the rows it counts', async () => {
    renderView();
    const strip = await screen.findByTestId('dupes-labels-stats');
    fireEvent.click(within(strip).getByRole('button', { name: 'true_dup: 2' }));
    await waitFor(() => expect(listUrls().at(-1)).toContain('label=true_dup'));
    expect(listUrls().at(-1)).not.toContain('label_source=');
  });

  it('a source count replaces the earlier filter instead of stacking on it', async () => {
    renderView();
    const strip = await screen.findByTestId('dupes-labels-stats');
    fireEvent.click(within(strip).getByRole('button', { name: 'true_dup: 2' }));
    await waitFor(() => expect(listUrls().at(-1)).toContain('label=true_dup'));

    fireEvent.click(within(strip).getByRole('button', { name: 'human: 2' }));

    await waitFor(() => expect(listUrls().at(-1)).toContain('label_source=human'));
    expect(listUrls().at(-1)).not.toContain('label=true_dup');
  });

  it('the total count clears every filter', async () => {
    renderView();
    const strip = await screen.findByTestId('dupes-labels-stats');
    fireEvent.click(within(strip).getByRole('button', { name: 'unsure: 1' }));
    await waitFor(() => expect(listUrls().at(-1)).toContain('label=unsure'));

    fireEvent.click(within(strip).getByRole('button', { name: 'total 5' }));

    await waitFor(() => expect(listUrls().at(-1)).not.toContain('label='));
  });

  it('a count clicked on the Suspicious view lands on the Labels rows', async () => {
    renderView('suspicious');
    const strip = await screen.findByTestId('dupes-labels-stats');
    expect(screen.getByTestId('current-view')).toHaveTextContent('suspicious');

    fireEvent.click(within(strip).getByRole('button', { name: 'unsure: 1' }));

    await waitFor(() => expect(screen.getByTestId('current-view')).toHaveTextContent('labels'));
    await waitFor(() => expect(listUrls().at(-1)).toContain('label=unsure'));
  });

  it('says counts are unavailable when the request fails, and still lists the rows', async () => {
    respond.stats = () => json({}, 503);
    renderView();
    expect(await screen.findByText(/Label counts unavailable: HTTP 503/)).toBeInTheDocument();
    expect(await screen.findByText('Book One')).toBeInTheDocument();
  });
});

describe('DupesLabelsView: score evidence', () => {
  it('shows the stored score breakdown for a row on demand', async () => {
    renderView();
    await screen.findByText('Book One');
    expect(screen.queryByText('Exact file hash')).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: /score breakdown for pair 42/i }));

    expect(await screen.findByText('Exact file hash')).toBeInTheDocument();
  });

  it('explains a row with no recorded breakdown instead of showing a blank panel', async () => {
    const { score: _s, score_breakdown: _b, ...bare } = sampleRow;
    respond.labels = () => json({ data: { labels: [bare], total: 1 } });
    renderView();
    await screen.findByText('Book One');

    fireEvent.click(screen.getByRole('button', { name: /score breakdown for pair 42/i }));

    expect(await screen.findByText(/rule that matched it was not recorded/i)).toBeInTheDocument();
  });
});

describe('DupesLabelsView: Suspicious', () => {
  it('renders suspicious rows and their reasons', async () => {
    renderView('suspicious');
    expect(await screen.findByText('cosine similarity >= 0.95')).toBeInTheDocument();
    expect(screen.getByText('duplicate-likely band (CERTAIN)')).toBeInTheDocument();
    expect(screen.getAllByText('Book Two').length).toBeGreaterThan(0);
    expect(fetchMock.mock.calls.some(([u]) => String(u).includes('/dedup/labels/suspicious'))).toBe(
      true
    );
  });

  it('is empty, loading, and error as three different things', async () => {
    respond.suspicious = () => json({ data: { labels: [], total: 0 } });
    const first = renderView('suspicious');
    expect(screen.getByLabelText('Loading suspicious labels')).toBeInTheDocument();
    expect(await screen.findByText('No suspicious labels in the queue.')).toBeInTheDocument();
    first.unmount();

    respond.suspicious = () => json({}, 502);
    renderView('suspicious');
    expect(await screen.findByText('HTTP 502')).toBeInTheDocument();
    expect(screen.queryByText('No suspicious labels in the queue.')).not.toBeInTheDocument();
  });

  it('posts an override to the existing route and drops the row', async () => {
    renderView('suspicious');
    await screen.findByText('cosine similarity >= 0.95');

    fireEvent.click(screen.getByRole('button', { name: /Mark true_dup/i }));

    await waitFor(() => {
      const call = fetchMock.mock.calls.find(([u]) =>
        String(u).includes('/dedup/labels/77/override')
      );
      expect(call).toBeTruthy();
      expect(JSON.parse((call![1] as RequestInit).body as string)).toMatchObject({
        label: 'true_dup',
      });
    });
    await waitFor(() =>
      expect(screen.queryByText('cosine similarity >= 0.95')).not.toBeInTheDocument()
    );
  });
});

describe('DupesLabelsView: columns saved by the old Gold Labels page', () => {
  it('still shows the Score / Why column when the old page saved a column list without it', async () => {
    // pages/DedupLabels.tsx saves its visible columns under table_config_dedup-labels.
    // This view must not read that key: it has no Score column, so a list saved
    // there would hide this view's.
    const oldPage = JSON.stringify({
      visible: ['book_a', 'book_b', 'layer', 'band', 'source', 'reason', 'label'],
      widths: {},
    });
    localStorage.setItem('table_config_dedup-labels', oldPage);

    renderView();

    expect(
      await screen.findByRole('button', { name: /score breakdown for pair 42/i })
    ).toBeVisible();
    // Nor does this view write over the page's saved list.
    expect(localStorage.getItem('table_config_dedup-labels')).toBe(oldPage);
  });
});

describe('DupesLabelsView: unlabeled rows and the full filter lists', () => {
  it('opens exactly the unlabeled rows from the unlabeled count', async () => {
    const unlabeledRow = (id: number): LabeledExample => ({
      ...sampleRow,
      candidate_id: id,
      label: '',
      a: { title: `Book Three ${id}` },
      b: { title: `Book Three ${id} (CD)` },
    });
    fetchMock.mockImplementation((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes('/config')) return json({ data: { config: { root_dir: LIBROOT } } });
      if (url.includes('/dedup/labels/stats')) return respond.stats();
      if (url.includes('/dedup/labels?'))
        return url.includes('label=unlabeled')
          ? json({ data: { labels: [unlabeledRow(11), unlabeledRow(12)], total: 2 } })
          : respond.labels();
      return json({ data: {} });
    });
    renderView();
    const strip = await screen.findByTestId('dupes-labels-stats');

    fireEvent.click(within(strip).getByRole('button', { name: 'unlabeled: 2' }));

    expect(await screen.findByText('Book Three 11')).toBeInTheDocument();
    expect(screen.getByText('Book Three 12')).toBeInTheDocument();
    expect(screen.queryByText('Book One')).not.toBeInTheDocument();
    expect(screen.getByText('1–2 of 2')).toBeInTheDocument();
  });

  it('offers every label and source the stats can report', async () => {
    renderView();
    await screen.findByText('Book One');

    fireEvent.mouseDown(screen.getByLabelText('Label'));
    expect(screen.getByRole('option', { name: 'unlabeled' })).toBeInTheDocument();
    fireEvent.click(screen.getByRole('option', { name: 'unlabeled' }));
    await waitFor(() => expect(listUrls().at(-1)).toContain('label=unlabeled'));

    fireEvent.mouseDown(screen.getByLabelText('Source'));
    fireEvent.click(await screen.findByRole('option', { name: 'itunes_attr' }));
    await waitFor(() => expect(listUrls().at(-1)).toContain('label_source=itunes_attr'));
  });
});

describe('DupesLabelsView: applied count, accessibility, in-flight, retry', () => {
  it('marks the count whose rows are showing, and only that one', async () => {
    renderView();
    const strip = await screen.findByTestId('dupes-labels-stats');
    const chip = (name: string) => within(strip).getByRole('button', { name });
    await waitFor(() => expect(chip('total 5')).toHaveAttribute('aria-pressed', 'true'));

    fireEvent.click(chip('unsure: 1'));

    await waitFor(() => expect(chip('unsure: 1')).toHaveAttribute('aria-pressed', 'true'));
    expect(chip('total 5')).toHaveAttribute('aria-pressed', 'false');
    expect(chip('true_dup: 2')).toHaveAttribute('aria-pressed', 'false');
  });

  it('names the Why button with its visible text and points it at the panel it opens', async () => {
    renderView();
    const why = await screen.findByRole('button', { name: /^71 · Why/ });
    expect(why).not.toHaveAttribute('aria-controls');

    fireEvent.click(why);

    const panelId = why.getAttribute('aria-controls');
    expect(panelId).toBeTruthy();
    expect(await screen.findByText('Exact file hash')).toBeInTheDocument();
    expect(document.getElementById(panelId!)).toContainElement(screen.getByText('Exact file hash'));
  });

  it('lets the view fill the lane and scroll inside it', async () => {
    renderView();
    const root = await screen.findByTestId('dupes-labels-view');
    expect(root).toHaveStyle({ flexGrow: '1', minHeight: '0px' });
  });

  it("disables a row's label toggle while its override is in flight", async () => {
    let release!: (r: Response) => void;
    const base = fetchMock.getMockImplementation() as (
      i: RequestInfo | URL,
      init?: RequestInit
    ) => Promise<Response>;
    fetchMock.mockImplementation((input: RequestInfo | URL, init?: RequestInit) =>
      String(input).includes('/override')
        ? new Promise<Response>((resolve) => (release = resolve))
        : base(input, init)
    );
    renderView();
    await screen.findByText('Book One');

    fireEvent.click(screen.getByRole('button', { name: /^not$/i }));

    await waitFor(() => expect(screen.getByRole('button', { name: /^not$/i })).toBeDisabled());
    release(await json({ data: { label: 'not_dup', label_source: 'human' } }));
    await waitFor(() => expect(screen.getByRole('button', { name: /^not$/i })).toBeEnabled());
  });

  it("disables both Suspicious buttons while that row's override is in flight", async () => {
    let release!: (r: Response) => void;
    const base = fetchMock.getMockImplementation() as (
      i: RequestInfo | URL,
      init?: RequestInit
    ) => Promise<Response>;
    fetchMock.mockImplementation((input: RequestInfo | URL, init?: RequestInit) =>
      String(input).includes('/override')
        ? new Promise<Response>((resolve) => (release = resolve))
        : base(input, init)
    );
    renderView('suspicious');
    await screen.findByText('cosine similarity >= 0.95');

    fireEvent.click(screen.getByRole('button', { name: /Mark true_dup/i }));

    await waitFor(() =>
      expect(screen.getByRole('button', { name: /Mark true_dup/i })).toBeDisabled()
    );
    expect(screen.getByRole('button', { name: /Confirm not_dup/i })).toBeDisabled();
    release(await json({ data: {} }));
    await waitFor(() =>
      expect(screen.queryByText('cosine similarity >= 0.95')).not.toBeInTheDocument()
    );
  });

  it('retries the counts after they failed', async () => {
    respond.stats = () => json({}, 503);
    renderView();
    expect(await screen.findByText(/Label counts unavailable/)).toBeInTheDocument();

    respond.stats = () => json({ data: STATS });
    fireEvent.click(screen.getByRole('button', { name: 'Retry counts' }));

    expect(await screen.findByTestId('dupes-labels-stats')).toBeInTheDocument();
  });

  it('shows how many of the whole suspicious queue are on the page', async () => {
    respond.suspicious = () => json({ data: { labels: [suspiciousRow], total: 3 } });
    renderView('suspicious');
    expect(await screen.findByText('1 of 3 suspicious')).toBeInTheDocument();
  });
});

describe('DupesLabelsView: band debounce', () => {
  it('sends nothing while the band is still being typed, then one request when it settles', async () => {
    debounce.held = true;
    renderView();
    await screen.findByText('Book One');
    const before = listUrls().length;

    fireEvent.change(screen.getByLabelText('Band'), { target: { value: 'title_author' } });
    expect(screen.getByLabelText('Band')).toHaveValue('title_author');
    expect(listUrls()).toHaveLength(before);

    debounce.held = false;
    fireEvent.click(screen.getByRole('button', { name: 'rerender' }));

    await waitFor(() => expect(listUrls()).toHaveLength(before + 1));
    expect(listUrls().at(-1)).toContain('band=title_author');
    await screen.findByText('Book One');
    expect(listUrls()).toHaveLength(before + 1);
  });
});

describe('DupesLabelsView: pager after a row left the page', () => {
  it('Next continues from the end of the rows shown, not a fixed page ahead', async () => {
    // A short page of 2 with 3 matching: one row was overridden out, so the
    // unseen row has moved up to offset 2. Skipping to a full page would lose it.
    respond.labels = () =>
      json({ data: { labels: [sampleRow, { ...sampleRow, candidate_id: 43 }], total: 3 } });
    renderView();
    await screen.findAllByText('Book One');
    expect(screen.getByText('1–2 of 3')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Next' }));

    await waitFor(() => expect(listUrls().at(-1)).toContain('offset=2'));
  });
});
