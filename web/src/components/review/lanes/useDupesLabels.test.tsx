// file: web/src/components/review/lanes/useDupesLabels.test.tsx
// version: 1.1.0
// guid: 2f7c4a19-8e53-4d60-b1a8-6c0e9d3b5f47
// last-edited: 2026-10-13
//
// The Labels / Suspicious data layer against a stubbed fetch. Ported from the
// data-facing cases of pages/__tests__/DedupLabels.test.tsx (filters sent as
// query params, override POST, suspicious fetch + override) and extended to the
// four states every lane fetch has.
//
// The band debounce is replaced with identity: its timing is useDebouncedSearch's
// own concern, and a test that waits on a 300ms timer is the kind of flake the
// dupes lane has had before. Everything here waits on a request or a state.

import { act, renderHook, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

// Pass-through by default. `held` freezes the settled value where it is, which is
// what the real hook looks like during its 300ms wait: the box has moved on, the
// settled twin has not. A test releases it and re-renders, so nothing waits on a
// timer.
const debounce = vi.hoisted(() => ({ held: false }));
vi.mock('../../../hooks/useDebouncedSearch', async () => {
  const { useRef } = await import('react');
  return {
    useDebouncedSearch: (value: string) => {
      const settled = useRef('');
      if (!debounce.held) settled.current = value;
      return settled.current;
    },
  };
});
vi.mock('../../../utils/apiFetch', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../../utils/apiFetch')>();
  return { ...actual, apiFetch: vi.fn(actual.apiFetch) };
});

import { ApiTimeoutError, apiFetch } from '../../../utils/apiFetch';
import {
  LABELS_PAGE,
  LABELS_TIMEOUT_MS,
  parseDupesSubView,
  useDupesLabels,
  type DupesSubView,
  type LabeledExample,
  type SuspiciousExample,
} from './useDupesLabels';

function json(body: unknown, status = 200) {
  return Promise.resolve({
    ok: status < 400,
    status,
    json: () => Promise.resolve(body),
  } as Response);
}

const row = (id: number, over: Partial<LabeledExample> = {}): LabeledExample => ({
  candidate_id: id,
  entity_a_id: `A${id}`,
  entity_b_id: `B${id}`,
  layer: 'exact',
  band: 'HIGH',
  label: 'unsure',
  label_source: 'rule',
  label_reason: 'part_vs_whole',
  a: { title: `Book One ${id}` },
  b: { title: `Book One ${id} (copy)` },
  ...over,
});

const suspicious = (id: number): SuspiciousExample => ({
  ...row(id, { label: 'not_dup' }),
  suspicion_reasons: ['duplicate-likely band (CERTAIN)'],
});

interface Server {
  labels: () => Promise<Response>;
  suspicious: () => Promise<Response>;
  stats: () => Promise<Response>;
  override: (id: string, body: unknown) => Promise<Response>;
}

let fetchMock: ReturnType<typeof vi.fn>;
let server: Server;

const urls = () => fetchMock.mock.calls.map(([u]) => String(u));
const listUrls = () => urls().filter((u) => u.includes('/dedup/labels?'));

beforeEach(() => {
  server = {
    labels: () => json({ data: { labels: [row(1), row(2)], total: 2 } }),
    suspicious: () => json({ data: { labels: [suspicious(7)], total: 1 } }),
    stats: () =>
      json({ data: { total: 2, by_label: { unsure: 2 }, by_source: { rule: 2, human: 0 } } }),
    override: () => json({ data: { status: 'updated', label: 'not_dup', label_source: 'human' } }),
  };
  fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url.includes('/override')) return server.override(url, init?.body);
    if (url.includes('/dedup/labels/stats')) return server.stats();
    if (url.includes('/dedup/labels/suspicious')) return server.suspicious();
    if (url.includes('/dedup/labels')) return server.labels();
    return json({ data: {} });
  });
  vi.stubGlobal('fetch', fetchMock);
  vi.mocked(apiFetch).mockClear();
});

afterEach(() => {
  debounce.held = false;
  vi.unstubAllGlobals();
  vi.clearAllMocks();
});

function mount(initial: DupesSubView) {
  return renderHook(({ view }) => useDupesLabels(view), { initialProps: { view: initial } });
}

describe('parseDupesSubView', () => {
  it('accepts labels and suspicious, and falls back to queue for anything else', () => {
    expect(parseDupesSubView('labels')).toBe('labels');
    expect(parseDupesSubView('suspicious')).toBe('suspicious');
    expect(parseDupesSubView('queue')).toBe('queue');
    expect(parseDupesSubView('bogus')).toBe('queue');
    expect(parseDupesSubView(null)).toBe('queue');
  });
});

describe('useDupesLabels', () => {
  it('requests nothing while the Queue sub-view is showing', async () => {
    const { result } = mount('queue');
    // Let any (wrongly) scheduled effect run before asserting there was no call.
    await act(async () => {});
    expect(fetchMock).not.toHaveBeenCalled();
    expect(result.current.status).toBe('loading');
  });

  it('is loading, then populated with the page and its counts', async () => {
    const { result } = mount('labels');
    expect(result.current.status).toBe('loading');

    await waitFor(() => expect(result.current.status).toBe('populated'));
    expect(result.current.rows.map((r) => r.candidate_id)).toEqual([1, 2]);
    expect(result.current.total).toBe(2);
    await waitFor(() => expect(result.current.stats.state).toBe('ready'));
    const first = new URL(listUrls()[0], 'http://x');
    expect(first.searchParams.get('limit')).toBe(String(LABELS_PAGE));
    expect(first.searchParams.get('offset')).toBe('0');
  });

  it('is empty when the server returns no rows', async () => {
    server.labels = () => json({ data: { labels: [], total: 0 } });
    const { result } = mount('labels');
    await waitFor(() => expect(result.current.status).toBe('empty'));
    expect(result.current.error).toBeNull();
  });

  it('reports an error rather than an empty list, and reload recovers', async () => {
    server.labels = () => json({}, 500);
    const { result } = mount('labels');
    await waitFor(() => expect(result.current.status).toBe('error'));
    expect(result.current.error).toBe('HTTP 500');

    server.labels = () => json({ data: { labels: [row(3)], total: 1 } });
    act(() => result.current.reload());
    await waitFor(() => expect(result.current.status).toBe('populated'));
    expect(result.current.rows.map((r) => r.candidate_id)).toEqual([3]);
  });

  it('keeps a failed count request separate from the list', async () => {
    server.stats = () => json({}, 503);
    const { result } = mount('labels');
    await waitFor(() => expect(result.current.stats.state).toBe('error'));
    await waitFor(() => expect(result.current.status).toBe('populated'));
  });

  it('sends label, source, and band filters as query params', async () => {
    const { result } = mount('labels');
    await waitFor(() => expect(result.current.status).toBe('populated'));

    act(() => {
      result.current.setLabelFilter('true_dup');
      result.current.setSourceFilter('human');
      result.current.setBandInput('title_author');
    });

    await waitFor(() => {
      const url = listUrls().at(-1) ?? '';
      expect(url).toContain('label=true_dup');
      expect(url).toContain('label_source=human');
      expect(url).toContain('band=title_author');
    });
  });

  it('returns to page one when a filter changes, in a single request', async () => {
    server.labels = () => json({ data: { labels: [row(1)], total: 120 } });
    const { result } = mount('labels');
    await waitFor(() => expect(result.current.status).toBe('populated'));
    act(() => result.current.setOffset(LABELS_PAGE));
    await waitFor(() => expect(listUrls().at(-1)).toContain(`offset=${LABELS_PAGE}`));

    const before = listUrls().length;
    act(() => result.current.setLabelFilter('not_dup'));
    await waitFor(() => expect(listUrls().length).toBe(before + 1));
    expect(listUrls().at(-1)).toContain('offset=0');
    expect(listUrls().at(-1)).toContain('label=not_dup');
    expect(result.current.offset).toBe(0);
  });

  it('showOnly replaces every filter with exactly the clicked one', async () => {
    const { result } = mount('labels');
    await waitFor(() => expect(result.current.status).toBe('populated'));
    act(() => {
      result.current.setLabelFilter('true_dup');
      result.current.setBandInput('HIGH');
    });
    await waitFor(() => expect(listUrls().at(-1)).toContain('band=HIGH'));

    act(() => result.current.showOnly({ source: 'human' }));

    await waitFor(() => {
      const url = listUrls().at(-1) ?? '';
      expect(url).toContain('label_source=human');
      expect(url).not.toContain('label=');
      expect(url).not.toContain('band=');
    });
    expect(result.current.bandInput).toBe('');
  });

  it('posts an override, updates the row in place, and refreshes the counts', async () => {
    const { result } = mount('labels');
    await waitFor(() => expect(result.current.status).toBe('populated'));
    await waitFor(() => expect(result.current.stats.state).toBe('ready'));
    const statsBefore = urls().filter((u) => u.includes('/labels/stats')).length;

    await act(async () => {
      await result.current.override(1, 'not_dup');
    });

    const call = fetchMock.mock.calls.find(([u]) => String(u).includes('/dedup/labels/1/override'));
    expect(call).toBeTruthy();
    expect(JSON.parse((call![1] as RequestInit).body as string)).toMatchObject({
      label: 'not_dup',
      reason: 'ui_override',
    });
    const updated = result.current.rows.find((r) => r.candidate_id === 1)!;
    expect(updated).toMatchObject({ label: 'not_dup', label_source: 'human' });
    // The other row is untouched.
    expect(result.current.rows.find((r) => r.candidate_id === 2)!.label).toBe('unsure');
    await waitFor(() =>
      expect(urls().filter((u) => u.includes('/labels/stats')).length).toBe(statsBefore + 1)
    );
  });

  it('surfaces a failed override and leaves the row as it was', async () => {
    server.override = () => json({}, 500);
    const { result } = mount('labels');
    await waitFor(() => expect(result.current.status).toBe('populated'));

    await act(async () => {
      await result.current.override(1, 'not_dup');
    });

    expect(result.current.actionError).toBe('HTTP 500');
    expect(result.current.rows.find((r) => r.candidate_id === 1)!.label).toBe('unsure');
  });

  it('loads the suspicious queue with its reasons, and an override removes the row', async () => {
    const { result } = mount('suspicious');
    expect(result.current.suspiciousStatus).toBe('loading');
    await waitFor(() => expect(result.current.suspiciousStatus).toBe('populated'));
    expect(result.current.suspiciousRows[0].suspicion_reasons).toEqual([
      'duplicate-likely band (CERTAIN)',
    ]);
    // Only the suspicious route is read; the label list is not.
    expect(listUrls()).toHaveLength(0);

    await act(async () => {
      await result.current.overrideSuspicious(7, 'true_dup');
    });

    const call = fetchMock.mock.calls.find(([u]) => String(u).includes('/dedup/labels/7/override'));
    expect(JSON.parse((call![1] as RequestInit).body as string)).toMatchObject({
      label: 'true_dup',
    });
    expect(result.current.suspiciousRows).toEqual([]);
    expect(result.current.suspiciousStatus).toBe('empty');
  });

  it('reports a suspicious-queue error as an error, not as an empty queue', async () => {
    server.suspicious = () => json({}, 502);
    const { result } = mount('suspicious');
    await waitFor(() => expect(result.current.suspiciousStatus).toBe('error'));
    expect(result.current.suspiciousError).toBe('HTTP 502');
  });

  it('aborts the list request when the view is left before it answers', async () => {
    let signal: AbortSignal | undefined;
    server.labels = () => new Promise<Response>(() => {});
    fetchMock.mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.includes('/dedup/labels?')) signal = init?.signal ?? undefined;
      return url.includes('/dedup/labels?') ? server.labels() : json({ data: {} });
    });
    const { rerender } = mount('labels');
    await waitFor(() => expect(signal).toBeDefined());
    expect(signal!.aborted).toBe(false);

    rerender({ view: 'queue' });

    expect(signal!.aborted).toBe(true);
  });

  it('shows loading, not the previous page, while a changed filter is fetched', async () => {
    const { result } = mount('labels');
    await waitFor(() => expect(result.current.status).toBe('populated'));

    let release!: (r: Response) => void;
    server.labels = () => new Promise<Response>((resolve) => (release = resolve));
    act(() => result.current.setLabelFilter('true_dup'));

    await waitFor(() => expect(result.current.status).toBe('loading'));
    expect(result.current.rows).toEqual([]);

    release(await json({ data: { labels: [row(9, { label: 'true_dup' })], total: 1 } }));
    await waitFor(() => expect(result.current.status).toBe('populated'));
    expect(result.current.rows.map((r) => r.candidate_id)).toEqual([9]);
  });

  it('sends no request for a typed band until the debounce settles, then exactly one', async () => {
    debounce.held = true;
    const { result, rerender } = mount('labels');
    await waitFor(() => expect(result.current.status).toBe('populated'));
    const before = listUrls().length;

    act(() => result.current.setBandInput('title_author'));
    await act(async () => {});
    expect(result.current.bandInput).toBe('title_author');
    expect(result.current.filters.band).toBe('');
    expect(listUrls()).toHaveLength(before);

    debounce.held = false;
    rerender({ view: 'labels' });
    await waitFor(() => expect(listUrls()).toHaveLength(before + 1));
    expect(listUrls().at(-1)).toContain('band=title_author');
    await act(async () => {});
    expect(listUrls()).toHaveLength(before + 1);
  });

  it('showOnly while a band is applied does not send a request with the old band', async () => {
    const { result } = mount('labels');
    await waitFor(() => expect(result.current.status).toBe('populated'));
    act(() => result.current.setBandInput('HIGH'));
    await waitFor(() => expect(listUrls().at(-1)).toContain('band=HIGH'));

    // The settled twin lags the emptied box, as the real debounce does.
    debounce.held = true;
    const before = listUrls().length;
    act(() => result.current.showOnly({ label: 'unsure' }));
    await waitFor(() => expect(listUrls().length).toBeGreaterThan(before));
    await act(async () => {});

    const sent = listUrls().slice(before);
    expect(sent).toHaveLength(1);
    expect(sent[0]).toContain('label=unsure');
    expect(sent[0]).not.toContain('band=');
  });

  it('drops an overridden row that no longer matches the label filter, and the total follows', async () => {
    server.labels = () => json({ data: { labels: [row(1), row(2)], total: 2 } });
    const { result } = mount('labels');
    act(() => result.current.setLabelFilter('unsure'));
    await waitFor(() => expect(listUrls().at(-1)).toContain('label=unsure'));
    await waitFor(() => expect(result.current.rows).toHaveLength(2));

    await act(async () => {
      await result.current.override(1, 'not_dup');
    });

    expect(result.current.rows.map((r) => r.candidate_id)).toEqual([2]);
    expect(result.current.total).toBe(1);
  });

  it('keeps an overridden row that still matches the filters', async () => {
    const { result } = mount('labels');
    act(() => result.current.setSourceFilter('human'));
    await waitFor(() => expect(listUrls().at(-1)).toContain('label_source=human'));
    await waitFor(() => expect(result.current.rows).toHaveLength(2));

    await act(async () => {
      await result.current.override(1, 'not_dup');
    });

    expect(result.current.rows).toHaveLength(2);
    expect(result.current.total).toBe(2);
  });

  it('refetches when an override empties a page that has more rows behind it', async () => {
    server.labels = () => json({ data: { labels: [row(1)], total: 5 } });
    const { result } = mount('labels');
    act(() => result.current.setLabelFilter('unsure'));
    await waitFor(() => expect(result.current.rows).toHaveLength(1));
    await waitFor(() => expect(listUrls().at(-1)).toContain('label=unsure'));
    const before = listUrls().length;
    server.labels = () => json({ data: { labels: [row(4)], total: 4 } });

    await act(async () => {
      await result.current.override(1, 'not_dup');
    });

    await waitFor(() => expect(listUrls().length).toBe(before + 1));
    await waitFor(() => expect(result.current.rows.map((r) => r.candidate_id)).toEqual([4]));
    expect(result.current.total).toBe(4);
  });

  it('keeps the suspicious queue total, and refills the page when overrides empty it', async () => {
    server.suspicious = () => json({ data: { labels: [suspicious(7)], total: 3 } });
    const { result } = mount('suspicious');
    await waitFor(() => expect(result.current.suspiciousStatus).toBe('populated'));
    expect(result.current.suspiciousTotal).toBe(3);
    const susp = () => urls().filter((u) => u.includes('/labels/suspicious')).length;
    const before = susp();
    server.suspicious = () => json({ data: { labels: [suspicious(8)], total: 2 } });

    await act(async () => {
      await result.current.overrideSuspicious(7, 'true_dup');
    });

    // Not "empty": 2 are still waiting, so the page is fetched again.
    await waitFor(() => expect(susp()).toBe(before + 1));
    await waitFor(() =>
      expect(result.current.suspiciousRows.map((r) => r.candidate_id)).toEqual([8])
    );
    expect(result.current.suspiciousTotal).toBe(2);
  });

  it('sends every read with the lane timeout', async () => {
    const stats = mount('suspicious');
    await waitFor(() => expect(stats.result.current.suspiciousStatus).toBe('populated'));
    const list = mount('labels');
    await waitFor(() => expect(list.result.current.status).toBe('populated'));

    const reads = vi.mocked(apiFetch).mock.calls.filter(([, o]) => !o?.method);
    expect(reads.length).toBeGreaterThanOrEqual(3);
    for (const [, options] of reads) expect(options?.timeoutMs).toBe(LABELS_TIMEOUT_MS);
  });

  it('turns a timed-out read into the error state', async () => {
    vi.mocked(apiFetch).mockRejectedValueOnce(
      new ApiTimeoutError('/api/v1/dedup/labels/stats', LABELS_TIMEOUT_MS)
    );
    const { result } = mount('labels');
    await waitFor(() => expect(result.current.stats.state).toBe('error'));
    expect(result.current.stats.state === 'error' && result.current.stats.message).toMatch(
      /timed out/i
    );
    // Each read fails on its own: the list is unaffected.
    await waitFor(() => expect(result.current.status).toBe('populated'));
  });

  it('reloadStats asks for the counts again', async () => {
    server.stats = () => json({}, 503);
    const { result } = mount('labels');
    await waitFor(() => expect(result.current.stats.state).toBe('error'));

    server.stats = () => json({ data: { total: 1, by_label: {}, by_source: {} } });
    act(() => result.current.reloadStats());

    await waitFor(() => expect(result.current.stats.state).toBe('ready'));
  });

  it('disables a candidate while its override is in flight and sends one POST', async () => {
    let release!: (r: Response) => void;
    server.override = () => new Promise<Response>((resolve) => (release = resolve));
    const { result } = mount('labels');
    await waitFor(() => expect(result.current.status).toBe('populated'));

    let first!: Promise<void>;
    act(() => {
      first = result.current.override(1, 'not_dup');
    });
    await waitFor(() => expect(result.current.pending.has(1)).toBe(true));
    await act(async () => {
      await result.current.override(1, 'true_dup');
    });
    release(await json({ data: { label: 'not_dup', label_source: 'human' } }));
    await act(async () => {
      await first;
    });

    const posts = fetchMock.mock.calls.filter(([u]) => String(u).includes('/1/override'));
    expect(posts).toHaveLength(1);
    expect(result.current.pending.size).toBe(0);
  });

  it('shows a failed override only on the sub-view it happened on', async () => {
    server.override = () => json({}, 500);
    const { result, rerender } = mount('labels');
    await waitFor(() => expect(result.current.status).toBe('populated'));
    await act(async () => {
      await result.current.override(1, 'not_dup');
    });
    expect(result.current.actionError).toBe('HTTP 500');

    rerender({ view: 'suspicious' });
    expect(result.current.actionError).toBeNull();
    rerender({ view: 'labels' });
    expect(result.current.actionError).toBe('HTTP 500');
  });

  it('lets an override finish after the view is left: the write is never aborted', async () => {
    let release!: (r: Response) => void;
    server.override = () => new Promise<Response>((resolve) => (release = resolve));
    const { result, rerender } = mount('labels');
    await waitFor(() => expect(result.current.status).toBe('populated'));
    let done!: Promise<void>;
    act(() => {
      done = result.current.override(1, 'not_dup');
    });
    await waitFor(() => expect(result.current.pending.has(1)).toBe(true));

    rerender({ view: 'queue' });
    release(await json({ data: { label: 'not_dup', label_source: 'human' } }));
    await act(async () => {
      await done;
    });

    const call = fetchMock.mock.calls.find(([u]) => String(u).includes('/1/override'));
    expect((call![1] as RequestInit).signal).toBeFalsy();
    expect(result.current.pending.size).toBe(0);
    expect(result.current.actionError).toBeNull();
  });
});
