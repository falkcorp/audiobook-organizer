// file: web/src/components/dashboard/dashboardSplit.test.tsx
// version: 0.1.0
// guid: 0a33106a-e384-4089-8309-7e2473a60f25
// last-edited: 2026-11-21

// F9-04 slice 2: the Dashboard is split into the shell frame (heading, announcement banner, widget
// slot) and books' LibraryHomeWidget. pages/Dashboard.test.tsx pins the composed page's behaviour
// (it passed unchanged across the cut); this file pins the seam.

import { screen, waitFor } from '@testing-library/react';
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { renderWithProviders } from '../../test/renderWithProviders';
import * as api from '../../services/api';
import { Dashboard } from '../../pages/Dashboard';
import { LibraryHomeWidget } from '../../modules/books/pages/LibraryHomeWidget';
import { DashboardFrame } from './DashboardFrame';
import frameSource from './DashboardFrame.tsx?raw';

vi.mock('../../services/api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../services/api')>()),
  getSystemStatus: vi.fn(),
  getSystemStorage: vi.fn(),
  countAuthors: vi.fn(),
  countSeries: vi.fn(),
  countBooksFiltered: vi.fn(),
  startScan: vi.fn(),
  startOrganize: vi.fn(),
}));
vi.mock('../../stores/useOperationsStore', () => ({
  useOperationsStore: Object.assign(vi.fn(() => ({})), { getState: () => ({ startPolling: vi.fn() }) }),
}));
vi.mock('../AnnouncementBanner', () => ({
  AnnouncementBanner: () => <div data-testid="announcements" />,
}));

function neverLoad() {
  vi.mocked(api.getSystemStatus).mockReturnValue(new Promise(() => {}));
  vi.mocked(api.getSystemStorage).mockReturnValue(new Promise(() => {}));
  vi.mocked(api.countAuthors).mockReturnValue(new Promise(() => {}));
  vi.mocked(api.countSeries).mockReturnValue(new Promise(() => {}));
  vi.mocked(api.countBooksFiltered).mockReturnValue(new Promise(() => {}));
}

const follows = (a: HTMLElement, b: HTMLElement) => Boolean(a.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING);

describe('Dashboard split (F9-04 slice 2)', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    neverLoad();
  });

  it('the composed page shows the heading, then the banner, then the library tiles', () => {
    renderWithProviders(<Dashboard />);
    const heading = screen.getByRole('heading', { name: 'Dashboard' });
    const banner = screen.getByTestId('announcements');
    const tile = screen.getByText('Library Books');
    expect(follows(heading, banner)).toBe(true);
    expect(follows(banner, tile)).toBe(true);
  });

  it('the frame alone has the heading and banner and requests no library data', () => {
    renderWithProviders(
      <DashboardFrame>
        <p data-testid="slot">slot</p>
      </DashboardFrame>,
    );
    expect(screen.getByRole('heading', { name: 'Dashboard' })).toBeInTheDocument();
    expect(screen.getByTestId('announcements')).toBeInTheDocument();
    expect(follows(screen.getByTestId('announcements'), screen.getByTestId('slot'))).toBe(true);
    expect(api.getSystemStatus).not.toHaveBeenCalled();
    expect(api.countAuthors).not.toHaveBeenCalled();
  });

  it('the books widget alone has the tiles and loads the counts, but no page heading or banner', async () => {
    renderWithProviders(<LibraryHomeWidget />);
    expect(screen.getByText('Library Books')).toBeInTheDocument();
    await waitFor(() => expect(api.getSystemStatus).toHaveBeenCalled());
    expect(api.countAuthors).toHaveBeenCalled();
    expect(api.countSeries).toHaveBeenCalled();
    expect(screen.queryByRole('heading', { name: 'Dashboard' })).toBeNull();
    expect(screen.queryByTestId('announcements')).toBeNull();
  });

  it('the shell frame imports no books code', () => {
    const imports = frameSource.split('\n').filter((l) => /from '/.test(l));
    expect(imports.length).toBeGreaterThan(2);
    for (const l of imports) {
      expect(l).not.toMatch(/modules\/books|services\/api|LibraryHome/);
    }
  });
});
