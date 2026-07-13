// file: web/src/stores/useOperationsStore.test.ts
// version: 2.5.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-07-13

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { useOperationsStore } from './useOperationsStore';
import * as api from '../services/api';

// Mock the api module
vi.mock('../services/api');
vi.mock('./useAppStore', () => ({
  useAppStore: {
    getState: () => ({
      addNotification: vi.fn(),
    }),
  },
}));

describe('useOperationsStore', () => {
  beforeEach(() => {
    // Reset store to initial state
    useOperationsStore.setState({
      operations: {},
      activeOperations: [],
      polling: false,
    });
  });

  it('loadFromServer reads exclusively from v2 timeline endpoint', async () => {
    const v2Op: api.OperationV2 = {
      id: 'op-1',
      def_id: 'scan-def',
      plugin: 'library_scanner',
      display_name: 'Library Scan',
      status: 'running',
      priority: 10,
      notify_level: 0,
      progress_current: 75,
      progress_total: 100,
      progress_message: 'Almost done',
      current_phase: null,
      current_item: null,
      actor_user_id: null,
      parent_id: null,
      queued_at: '2026-05-06T09:00:00Z',
      started_at: '2026-05-06T09:01:00Z',
      completed_at: null,
      error_message: null,
      resume_count: 0,
      trace_id: null,
      span_id: null,
    };

    vi.mocked(api.getOperationTimeline).mockResolvedValue([v2Op]);

    await useOperationsStore.getState().loadFromServer();

    expect(api.getOperationTimeline).toHaveBeenCalledTimes(1);
    // v1 endpoints are deleted in UOS-14; only v2 timeline is called

    const ops = useOperationsStore.getState().activeOperations;
    expect(ops).toHaveLength(1);
    expect(ops[0].id).toBe('op-1');
    expect(ops[0].message).toBe('Almost done');
    expect(ops[0].progress).toBe(75);
  });

  it('stores parent_id and v2 fields from v2 operations', async () => {
    const parentV2: api.OperationV2 = {
      id: 'parent-op',
      def_id: 'scan-def',
      plugin: 'library_scanner',
      display_name: 'Library Scan',
      status: 'running',
      priority: 10,
      notify_level: 0,
      progress_current: 50,
      progress_total: 100,
      progress_message: 'Parent',
      current_phase: null,
      current_item: null,
      actor_user_id: null,
      parent_id: null,
      queued_at: '2026-05-06T09:00:00Z',
      started_at: '2026-05-06T09:01:00Z',
      completed_at: null,
      error_message: null,
      resume_count: 0,
      trace_id: null,
      span_id: null,
    };

    const childV2: api.OperationV2 = {
      id: 'child-op',
      def_id: 'index-def',
      plugin: 'indexer',
      display_name: 'Index',
      status: 'running',
      priority: 10,
      notify_level: 0,
      progress_current: 30,
      progress_total: 100,
      progress_message: 'Child',
      current_phase: 'phase-1',
      current_item: 'item-1',
      actor_user_id: null,
      parent_id: 'parent-op',
      queued_at: '2026-05-06T09:00:00Z',
      started_at: null,
      completed_at: null,
      error_message: null,
      resume_count: 0,
      trace_id: null,
      span_id: null,
    };

    vi.mocked(api.getOperationTimeline).mockResolvedValue([parentV2, childV2]);

    await useOperationsStore.getState().loadFromServer();

    const ops = useOperationsStore.getState().activeOperations;
    expect(ops).toHaveLength(2);
    const child = ops.find((op) => op.id === 'child-op');
    expect(child).toBeDefined();
    expect(child?.parent_id).toBe('parent-op');
    expect(child?.current_phase).toBe('phase-1');
    expect(child?.current_item).toBe('item-1');
  });

  it('gracefully handles timeline endpoint failure', async () => {
    vi.mocked(api.getOperationTimeline).mockRejectedValue(new Error('Network error'));

    // Should not throw, just log error
    await expect(useOperationsStore.getState().loadFromServer()).resolves.toBeUndefined();

    // Operations remain empty on failure
    expect(useOperationsStore.getState().activeOperations).toHaveLength(0);
  });

  it('startPolling inserts optimistic entry immediately', () => {
    vi.mocked(api.getOperationTimeline).mockResolvedValue([]);

    useOperationsStore.getState().startPolling('op-99', 'scan');

    const ops = useOperationsStore.getState().activeOperations;
    expect(ops).toHaveLength(1);
    expect(ops[0].id).toBe('op-99');
    expect(ops[0].status).toBe('queued');
    expect(ops[0].type).toBe('scan');
  });

  it('op.log SSE event updates latestLogEvent', () => {
    useOperationsStore.setState({ latestLogEvent: null, _sseSource: null } as Parameters<typeof useOperationsStore.setState>[0]);

    let capturedOnEvent: ((name: api.OperationSSEEventName, payload: unknown) => void) | null = null;
    vi.mocked(api.openOperationsSSE).mockImplementation(({ onEvent }) => {
      capturedOnEvent = onEvent;
      return {} as EventSource;
    });

    useOperationsStore.getState().openSSE();
    expect(capturedOnEvent).not.toBeNull();

    capturedOnEvent!('op.log', {
      op_id: 'test-op-log',
      message: 'Processing file 1',
      level: 'info',
      created_at: '2026-06-01T12:00:00Z',
    });

    const { latestLogEvent } = useOperationsStore.getState();
    expect(latestLogEvent).not.toBeNull();
    expect(latestLogEvent?.op_id).toBe('test-op-log');
    expect(latestLogEvent?.message).toBe('Processing file 1');
    expect(latestLogEvent?.level).toBe('info');
  });

  it('op.log SSE event with empty message is ignored', () => {
    useOperationsStore.setState({ latestLogEvent: null, _sseSource: null } as Parameters<typeof useOperationsStore.setState>[0]);

    let capturedOnEvent: ((name: api.OperationSSEEventName, payload: unknown) => void) | null = null;
    vi.mocked(api.openOperationsSSE).mockImplementation(({ onEvent }) => {
      capturedOnEvent = onEvent;
      return {} as EventSource;
    });

    useOperationsStore.getState().openSSE();

    capturedOnEvent!('op.log', {
      op_id: 'test-op-log',
      message: '',
      level: 'info',
      created_at: '2026-06-01T12:00:00Z',
    });

    expect(useOperationsStore.getState().latestLogEvent).toBeNull();
  });

  it('onError leaves a transiently-erroring (still auto-reconnecting) EventSource alone', () => {
    vi.useFakeTimers();
    useOperationsStore.setState({ _sseSource: null } as Parameters<typeof useOperationsStore.setState>[0]);
    vi.mocked(api.getOperationTimeline).mockResolvedValue([]);

    const closeSpy = vi.fn();
    // readyState CONNECTING (0): the browser is auto-reconnecting this exact
    // source on its own — matches a transient drop / server restart.
    const fakeSource = { close: closeSpy, readyState: EventSource.CONNECTING } as unknown as EventSource;
    let capturedOnError: ((err: Event) => void) | null = null;
    vi.mocked(api.openOperationsSSE).mockImplementation(({ onError }) => {
      capturedOnError = onError ?? null;
      return fakeSource;
    });

    useOperationsStore.getState().openSSE();
    expect(useOperationsStore.getState()._sseSource).toBe(fakeSource);

    capturedOnError!(new Event('error'));

    // Must NOT close or null the ref — openSSE() is only ever called once
    // per login (see App.tsx), so nulling here with nothing to re-open it
    // would kill live reconnect on the first blip. Leaving the ref intact
    // also means closeSSE() (e.g. on logout) can still reach and close this
    // same source, so there's no orphan.
    expect(closeSpy).not.toHaveBeenCalled();
    expect(useOperationsStore.getState()._sseSource).toBe(fakeSource);

    vi.useRealTimers();
  });

  it('onError closes and clears _sseSource once the EventSource has permanently given up (no orphaned reconnecting source)', () => {
    vi.useFakeTimers();
    useOperationsStore.setState({ _sseSource: null } as Parameters<typeof useOperationsStore.setState>[0]);
    vi.mocked(api.getOperationTimeline).mockResolvedValue([]);

    const closeSpy = vi.fn();
    // readyState CLOSED (2): the browser has given up for good — this is the
    // only case that should tear down and null the ref.
    const fakeSource = { close: closeSpy, readyState: EventSource.CLOSED } as unknown as EventSource;
    let capturedOnError: ((err: Event) => void) | null = null;
    vi.mocked(api.openOperationsSSE).mockImplementation(({ onError }) => {
      capturedOnError = onError ?? null;
      return fakeSource;
    });

    useOperationsStore.getState().openSSE();
    expect(useOperationsStore.getState()._sseSource).toBe(fakeSource);
    expect(capturedOnError).not.toBeNull();

    capturedOnError!(new Event('error'));

    expect(closeSpy).toHaveBeenCalledTimes(1);
    expect(useOperationsStore.getState()._sseSource).toBeNull();

    vi.useRealTimers();
  });

  it('a subsequent openSSE after a terminal error creates exactly one new EventSource', () => {
    useOperationsStore.setState({ _sseSource: null } as Parameters<typeof useOperationsStore.setState>[0]);
    vi.mocked(api.getOperationTimeline).mockResolvedValue([]);
    vi.mocked(api.openOperationsSSE).mockClear();

    let capturedOnError: ((err: Event) => void) | null = null;
    const firstSource = { close: vi.fn(), readyState: EventSource.CLOSED } as unknown as EventSource;
    const secondSource = { close: vi.fn(), readyState: EventSource.OPEN } as unknown as EventSource;
    vi.mocked(api.openOperationsSSE)
      .mockImplementationOnce(({ onError }) => {
        capturedOnError = onError ?? null;
        return firstSource;
      })
      .mockImplementationOnce(() => secondSource);

    useOperationsStore.getState().openSSE();
    capturedOnError!(new Event('error'));
    useOperationsStore.getState().openSSE();

    expect(api.openOperationsSSE).toHaveBeenCalledTimes(2);
    expect(useOperationsStore.getState()._sseSource).toBe(secondSource);
  });
});
