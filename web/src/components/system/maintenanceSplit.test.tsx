// file: web/src/components/system/maintenanceSplit.test.tsx
// version: 0.1.0
// guid: 0b6e0f2c-6d1a-4c53-9a7e-3f5b8d2c1a47
// last-edited: 2026-11-21

// F9-04 slice 1: MaintenanceTab is split into the shell half (the maintenance
// window and the registered-task controls) and the books half (the optimize,
// manual-fix, throttle, chapter, duplicate, AcoustID and hash jobs). The SPA
// must render the same Maintenance tab after the split, so the composed tab's
// card order is pinned here.

import { render, screen, waitFor } from '@testing-library/react';
import { describe, it, expect, vi, beforeEach } from 'vitest';
import * as api from '../../services/api';
import { MaintenanceTab } from './MaintenanceTab';
import { MaintenanceTabFrame } from './MaintenanceTabFrame';
import frameSource from './MaintenanceTabFrame.tsx?raw';
import { DuplicateAndHashJobs } from '../../modules/books/pages/DuplicateAndHashJobs';
import { OptimizeLibraryCard } from '../../modules/books/pages/OptimizeLibraryCard';

vi.mock('../../services/api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../services/api')>()),
  getMaintenanceWindowStatus: vi.fn(),
  updateMaintenanceWindowConfig: vi.fn(),
  runMaintenanceWindow: vi.fn(),
  getRegisteredTasks: vi.fn(),
  updateTaskConfig: vi.fn(),
  runTask: vi.fn(),
  startOptimize: vi.fn(),
  getBookFileHashStats: vi.fn(),
  getBookMetadataHashStats: vi.fn(),
  getAcoustIDStats: vi.fn(),
  listMaintenanceJobs: vi.fn(),
  runMaintenanceJob: vi.fn(),
  pollOperation: vi.fn(),
  getOperationResult: vi.fn(),
  getProviderThrottles: vi.fn(),
  startThrottleReset: vi.fn(),
  pollOperationV2: vi.fn(),
  scanDuplicateFiles: vi.fn(),
  backfillFileHashes: vi.fn(),
  backfillMetadataHashes: vi.fn(),
  findMetadataHashDuplicates: vi.fn(),
}));

function seedApi() {
  vi.mocked(api.getMaintenanceWindowStatus).mockResolvedValue({
    enabled: true,
    window_start: 1,
    window_end: 4,
    last_run_date: '',
    next_run_estimate: '',
    currently_running: false,
  });
  vi.mocked(api.getRegisteredTasks).mockResolvedValue([
    {
      name: 'gotham-sweep',
      description: 'Sweep the Gotham docks',
      category: 'library',
      enabled: true,
      interval_minutes: 60,
      run_on_startup: false,
      run_in_maintenance_window: true,
      is_running: false,
    },
  ]);
  vi.mocked(api.getBookFileHashStats).mockResolvedValue({
    total_book_files: 3,
    with_file_hash: 2,
    missing_file_hash: 1,
    with_original_hash: 0,
    total_books: 3,
    books_with_no_files: 0,
    by_library: [],
  });
  vi.mocked(api.getBookMetadataHashStats).mockResolvedValue({
    total_books: 3,
    with_metadata_hash: 2,
    missing_metadata_hash: 1,
    with_asin_or_isbn: 3,
    missing_hash_has_id: 1,
    by_library: [],
  });
  vi.mocked(api.getAcoustIDStats).mockResolvedValue({
    total_files: 3,
    with_fingerprint: 2,
    by_library: [],
  });
  vi.mocked(api.listMaintenanceJobs).mockResolvedValue([
    { id: 'cleanup-series', description: 'Tidy the rogues gallery', can_resume: false, default_params: { dry_run: true } },
  ]);
  vi.mocked(api.getProviderThrottles).mockResolvedValue([]);
}

/** Card titles and group headings of the rendered tab, in document order. */
function headings(container: HTMLElement): string[] {
  return Array.from(container.querySelectorAll('[class*="MuiCardHeader-title"], h6')).map((n) =>
    (n.textContent ?? '').trim(),
  );
}

describe('MaintenanceTab (F9-04 slice 1)', () => {
  beforeEach(() => {
    for (const fn of Object.values(api)) {
      if (typeof fn === 'function' && 'mockReset' in fn) (fn as ReturnType<typeof vi.fn>).mockReset();
    }
    seedApi();
  });

  it('renders every card and the task list in the SPA order', async () => {
    const { container } = render(<MaintenanceTab />);
    await screen.findByText('Sweep the Gotham docks');
    await waitFor(() => expect(api.getProviderThrottles).toHaveBeenCalled());
    expect(headings(container)).toEqual([
      '⚡ Optimize Library',
      'Maintenance Window',
      'Manual Fixes',
      'Provider Throttles',
      'Chapter Consolidation',
      'SHA Duplicate Detection',
      'Hash Coverage',
      'AcoustID Fingerprint Coverage',
      'Metadata Hash Duplicates',
      'Metadata Hash Coverage',
      'Library',
      'Sweep the Gotham docks',
    ]);
  });

  it('the shell half alone shows only the maintenance window and the registered tasks', async () => {
    const { container } = render(<MaintenanceTabFrame />);
    await screen.findByText('Sweep the Gotham docks');
    expect(headings(container)).toEqual(['Maintenance Window', 'Library', 'Sweep the Gotham docks']);
    // None of the books jobs is fetched by the shell half.
    expect(api.getProviderThrottles).not.toHaveBeenCalled();
    expect(api.listMaintenanceJobs).not.toHaveBeenCalled();
    expect(api.getAcoustIDStats).not.toHaveBeenCalled();
    expect(api.getBookFileHashStats).not.toHaveBeenCalled();
    expect(api.getBookMetadataHashStats).not.toHaveBeenCalled();
  });

  it('the frame renders `leading` above the window and `children` between the window and the tasks', async () => {
    const { container } = render(
      <MaintenanceTabFrame leading={<p data-testid="lead">lead</p>}>
        <p data-testid="mid">mid</p>
      </MaintenanceTabFrame>,
    );
    await screen.findByText('Sweep the Gotham docks');
    const order = (a: HTMLElement, b: HTMLElement) =>
      Boolean(a.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING);
    const windowTitle = screen.getByText('Maintenance Window');
    const taskTitle = screen.getByText('Sweep the Gotham docks');
    expect(order(screen.getByTestId('lead'), windowTitle)).toBe(true);
    expect(order(windowTitle, screen.getByTestId('mid'))).toBe(true);
    expect(order(screen.getByTestId('mid'), taskTitle)).toBe(true);
    expect(container.querySelectorAll('[data-testid="mid"]')).toHaveLength(1);
  });

  it('the books widgets carry the hash, AcoustID and duplicate jobs and the optimize card', async () => {
    const { container } = render(
      <>
        <OptimizeLibraryCard />
        <DuplicateAndHashJobs />
      </>,
    );
    await waitFor(() => expect(api.getAcoustIDStats).toHaveBeenCalled());
    expect(headings(container)).toEqual([
      '⚡ Optimize Library',
      'SHA Duplicate Detection',
      'Hash Coverage',
      'AcoustID Fingerprint Coverage',
      'Metadata Hash Duplicates',
      'Metadata Hash Coverage',
    ]);
    expect(api.getRegisteredTasks).not.toHaveBeenCalled();
    expect(api.getMaintenanceWindowStatus).not.toHaveBeenCalled();
  });

  it('the shell half imports no books code (the cut I5 will lint once it moves into core/web)', () => {
    const imports = frameSource.split('\n').filter((l) => /^import /.test(l) || /from '/.test(l));
    expect(imports.length).toBeGreaterThan(3);
    for (const l of imports) {
      expect(l).not.toMatch(/modules\/books|ThrottlePanel|ManualFixes|ChapterConsolidation/);
    }
    // Only the api module and MUI/React: the one ABO path it reaches is services/api, which the
    // flips replace with the native tasks routes before the file moves.
    expect(imports.filter((l) => l.includes("'.")).every((l) => l.includes('services/api'))).toBe(true);
  });
});
