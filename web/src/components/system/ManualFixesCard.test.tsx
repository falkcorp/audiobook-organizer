// file: web/src/components/system/ManualFixesCard.test.tsx
// version: 1.0.0
// guid: 43037c93-762f-4d99-9ae8-f7373f5c49fe
// last-edited: 2026-09-19

import { render, screen, waitFor, fireEvent, within } from '@testing-library/react';
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { ManualFixesCard } from './MaintenanceTab';
import * as api from '../../services/api';

vi.mock('../../services/api', () => ({
  listMaintenanceJobs: vi.fn(),
  runMaintenanceJob: vi.fn(),
  pollOperation: vi.fn(),
}));

const jobs: api.MaintenanceJobDef[] = [
  { id: 'plain-job', description: 'no dry run', can_resume: false, default_params: {} },
  {
    id: 'cleanup-series',
    description: 'dry run default',
    can_resume: false,
    default_params: { dry_run: true },
  },
  {
    id: 'merge-chapter-groups',
    description: 'chapter merge',
    can_resume: false,
    default_params: { dry_run: true },
  },
];

function row(jobId: string): HTMLElement {
  return screen.getByTestId(`manual-fix-${jobId}`);
}

describe('ManualFixesCard', () => {
  beforeEach(() => {
    vi.mocked(api.listMaintenanceJobs).mockResolvedValue(jobs);
    vi.mocked(api.runMaintenanceJob).mockReset();
    vi.mocked(api.runMaintenanceJob).mockResolvedValue({ operation_id: 'op-1' });
    vi.mocked(api.pollOperation).mockReset();
    vi.mocked(api.pollOperation).mockResolvedValue({
      id: 'op-1',
      status: 'completed',
      total: 42,
      progress: 42,
    } as unknown as Awaited<ReturnType<typeof api.pollOperation>>);
  });

  it('Run never sends dry_run:false — it omits dry_run so the advertised default applies', async () => {
    render(<ManualFixesCard />);
    await screen.findByTestId('manual-fix-plain-job');
    fireEvent.click(screen.getAllByRole('button', { name: /^Run$/ })[0]);
    await waitFor(() => expect(api.runMaintenanceJob).toHaveBeenCalled());
    expect(vi.mocked(api.runMaintenanceJob).mock.calls[0]).toEqual(['plain-job']);
  });

  it('a dry-run-default job previews first; a real run needs a confirm naming the count', async () => {
    render(<ManualFixesCard />);
    await screen.findByTestId('manual-fix-cleanup-series');
    const apply = () =>
      row('cleanup-series').querySelector('button[data-action="apply"]') as HTMLButtonElement;
    expect(apply()).toBeDisabled();

    fireEvent.click(
      row('cleanup-series').querySelector('button[data-action="preview"]') as HTMLButtonElement
    );
    await waitFor(() => expect(apply()).not.toBeDisabled());
    expect(vi.mocked(api.runMaintenanceJob).mock.calls[0]).toEqual(['cleanup-series']);

    fireEvent.click(apply());
    expect(within(await screen.findByRole('dialog')).getByText(/42 item/)).toBeInTheDocument();
    expect(api.runMaintenanceJob).toHaveBeenCalledTimes(1);
    fireEvent.click(screen.getByRole('button', { name: 'Run for real' }));
    await waitFor(() => expect(api.runMaintenanceJob).toHaveBeenCalledTimes(2));
    expect(vi.mocked(api.runMaintenanceJob).mock.calls[1]).toEqual(['cleanup-series', false]);
  });

  it('merge-chapter-groups cannot be applied here; it points at the card', async () => {
    render(<ManualFixesCard />);
    await screen.findByTestId('manual-fix-merge-chapter-groups');
    expect(row('merge-chapter-groups').querySelector('button[data-action="apply"]')).toBeNull();
    expect(row('merge-chapter-groups')).toHaveTextContent('Chapter Consolidation card');
  });
});
