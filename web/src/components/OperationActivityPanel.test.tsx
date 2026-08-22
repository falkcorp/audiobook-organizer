// file: web/src/components/OperationActivityPanel.test.tsx
// version: 1.1.0
// guid: c2e4a7b9-5d1f-4823-9a06-7b3e8c1d4f2a
// last-edited: 2026-08-22

import { render, screen, waitFor, act } from '@testing-library/react';
import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import { OperationActivityPanel } from './OperationActivityPanel';
import * as activityApi from '../services/activityApi';
import { useOperationsStore } from '../stores/useOperationsStore';

vi.mock('../services/activityApi', async () => {
  const actual = await vi.importActual<typeof activityApi>('../services/activityApi');
  return {
    ...actual,
    fetchOperationActivity: vi.fn(),
  };
});

describe('OperationActivityPanel', () => {
  afterEach(() => {
    vi.restoreAllMocks();
    vi.clearAllMocks();
  });

  beforeEach(() => {
    useOperationsStore.setState({ activeOperations: [], operations: {}, latestLogEvent: null }, false);
  });

  afterEach(() => {
    useOperationsStore.setState({ activeOperations: [], operations: {}, latestLogEvent: null }, false);
  });

  it('renders empty state when no entries', async () => {
    vi.mocked(activityApi.fetchOperationActivity).mockResolvedValue({
      operation_id: 'op-1',
      entries: [],
      total: 0,
    });

    render(<OperationActivityPanel operationId="op-1" />);

    await waitFor(() => {
      expect(screen.getByText(/No activity recorded for this operation yet/)).toBeInTheDocument();
    });
  });

  it('renders entries with level chips and message text', async () => {
    vi.mocked(activityApi.fetchOperationActivity).mockResolvedValue({
      operation_id: 'op-2',
      entries: [
        {
          timestamp: '2026-05-20T10:00:00Z',
          level: 'info',
          operation_id: 'op-2',
          operation_type: 'metadata-fetch',
          message: 'Started fetch',
        },
        {
          timestamp: '2026-05-20T10:00:05Z',
          level: 'error',
          operation_id: 'op-2',
          operation_type: 'metadata-fetch',
          message: 'Provider returned 503',
          details: 'request_id=abc123',
        },
      ],
      total: 2,
    });

    render(<OperationActivityPanel operationId="op-2" />);

    await waitFor(() => {
      expect(screen.getByText('Started fetch')).toBeInTheDocument();
      expect(screen.getByText('Provider returned 503')).toBeInTheDocument();
      expect(screen.getByText('2 entries')).toBeInTheDocument();
    });

    // Level chips render
    expect(screen.getByText('info')).toBeInTheDocument();
    expect(screen.getByText('error')).toBeInTheDocument();
  });

  it('renders error state on fetch failure', async () => {
    vi.mocked(activityApi.fetchOperationActivity).mockRejectedValue(new Error('Network down'));

    render(<OperationActivityPanel operationId="op-3" />);

    await waitFor(() => {
      expect(screen.getByText(/Network down/)).toBeInTheDocument();
    });
  });

  it('appends a live SSE log line exactly once, even when the op object changes shape on progress ticks with no new event', async () => {
    // Step 6: Mock the API to return empty state, then render and wait for load to finish
    vi.mocked(activityApi.fetchOperationActivity).mockResolvedValue({
      operation_id: 'op-9',
      entries: [],
      total: 0,
    });

    render(<OperationActivityPanel operationId="op-9" />);

    await waitFor(() => expect(activityApi.fetchOperationActivity).toHaveBeenCalled());

    // Step 6-continuation: Push one event and verify it appears
    act(() => {
      useOperationsStore.setState({
        latestLogEvent: {
          op_id: 'op-9',
          level: 'info',
          message: 'Scanning shelf 3',
          created_at: '2026-08-22T23:47:00Z',
          sequence: 1,
        },
      });
    });

    await screen.findByText('Scanning shelf 3');
    expect(screen.getByText('1 entry')).toBeInTheDocument();

    // Step 7: Update the op object TWICE with different progress values but the SAME latestLogEvent
    // This simulates progress ticks with no new log line
    act(() => {
      useOperationsStore.setState({
        activeOperations: [
          {
            id: 'op-9',
            type: 'scan',
            status: 'running',
            progress: 10,
            total: 100,
            message: '',
          },
        ],
      });
    });

    act(() => {
      useOperationsStore.setState({
        activeOperations: [
          {
            id: 'op-9',
            type: 'scan',
            status: 'running',
            progress: 20,
            total: 100,
            message: '',
          },
        ],
      });
    });

    // Count should STILL be exactly 1, and message should appear exactly once
    expect(screen.getByText('1 entry')).toBeInTheDocument();
    expect(screen.getAllByText('Scanning shelf 3')).toHaveLength(1);

    // Step 8: Push a second, distinct event
    act(() => {
      useOperationsStore.setState({
        latestLogEvent: {
          op_id: 'op-9',
          level: 'info',
          message: 'Shelf 3 complete',
          created_at: '2026-08-22T23:48:00Z',
          sequence: 2,
        },
      });
    });

    await screen.findByText('Shelf 3 complete');
    expect(screen.getByText('2 entries')).toBeInTheDocument();
  });
});
