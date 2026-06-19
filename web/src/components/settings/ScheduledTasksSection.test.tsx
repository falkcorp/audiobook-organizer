// file: web/src/components/settings/ScheduledTasksSection.test.tsx
// version: 1.0.0
// guid: e5f4a3b2-c1d0-9876-efab-c09876543210
// last-edited: 2026-06-19

import { render, screen, fireEvent } from '@testing-library/react';
import { describe, it, expect, vi } from 'vitest';
import { ScheduledTasksSection } from './ScheduledTasksSection';
import type { ScheduledTasksConfig } from '../../services/api';

const defaultTaskEntry = { enabled: false, interval: 60, on_startup: false };

const defaultConfig: ScheduledTasksConfig = {
  dedup_refresh: { ...defaultTaskEntry },
  author_split: { ...defaultTaskEntry },
  db_optimize: { ...defaultTaskEntry },
  metadata_refresh: { ...defaultTaskEntry },
  resolve_production_authors: { enabled: false, interval: 60 },
  series_prune: { ...defaultTaskEntry },
  ai_dedup_batch: { ...defaultTaskEntry },
  reconcile: { ...defaultTaskEntry },
};

describe('ScheduledTasksSection', () => {
  it('renders the section heading', () => {
    const onChange = vi.fn();
    render(<ScheduledTasksSection config={defaultConfig} onChange={onChange} />);
    expect(screen.getByText('Scheduled Tasks')).toBeInTheDocument();
  });

  it('renders the Dedup Refresh task label', () => {
    const onChange = vi.fn();
    render(<ScheduledTasksSection config={defaultConfig} onChange={onChange} />);
    expect(screen.getByText('Dedup Refresh')).toBeInTheDocument();
  });

  it('renders multiple Interval (minutes) fields', () => {
    const onChange = vi.fn();
    render(<ScheduledTasksSection config={defaultConfig} onChange={onChange} />);
    const intervalFields = screen.getAllByLabelText('Interval (minutes)');
    expect(intervalFields.length).toBeGreaterThan(0);
  });

  it('calls onChange with updated dedup_refresh.enabled when Dedup Refresh Enabled Switch is toggled', () => {
    const onChange = vi.fn();
    render(<ScheduledTasksSection config={defaultConfig} onChange={onChange} />);

    // The first "Enabled" switch belongs to Dedup Refresh (first task row)
    const enabledSwitches = screen.getAllByRole('checkbox', { name: /^enabled$/i });
    fireEvent.click(enabledSwitches[0]);

    expect(onChange).toHaveBeenCalledWith({
      dedup_refresh: { ...defaultConfig.dedup_refresh, enabled: true },
    });
  });

  it('calls onChange with numeric interval for dedup_refresh when interval field changes', () => {
    const onChange = vi.fn();
    render(<ScheduledTasksSection config={defaultConfig} onChange={onChange} />);

    const intervalFields = screen.getAllByLabelText('Interval (minutes)');
    fireEvent.change(intervalFields[0], { target: { value: '120' } });

    expect(onChange).toHaveBeenCalledWith({
      dedup_refresh: { ...defaultConfig.dedup_refresh, interval: 120 },
    });
    const callArg = onChange.mock.calls[0][0] as {
      dedup_refresh: { interval: unknown };
    };
    expect(typeof callArg.dedup_refresh.interval).toBe('number');
  });

  it('renders Author Split task label', () => {
    const onChange = vi.fn();
    render(<ScheduledTasksSection config={defaultConfig} onChange={onChange} />);
    expect(screen.getByText('Author Split')).toBeInTheDocument();
  });
});
