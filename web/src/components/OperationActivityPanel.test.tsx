// file: web/src/components/OperationActivityPanel.test.tsx
// version: 1.0.1
// guid: c2e4a7b9-5d1f-4823-9a06-7b3e8c1d4f2a

import { render, screen, waitFor } from '@testing-library/react';
import { describe, it, expect, vi, afterEach } from 'vitest';
import { OperationActivityPanel } from './OperationActivityPanel';
import * as activityApi from '../services/activityApi';

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
});
