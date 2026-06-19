// file: web/src/components/settings/MaintenanceSettingsSection.test.tsx
// version: 1.0.0
// guid: d6e5f4a3-b2c1-0987-defa-b09876543210
// last-edited: 2026-06-19

import { render, screen, fireEvent } from '@testing-library/react';
import { describe, it, expect, vi } from 'vitest';
import { MaintenanceSettingsSection } from './MaintenanceSettingsSection';
import type { MaintenanceConfig } from '../../services/api';

const defaultConfig: MaintenanceConfig = {
  enabled: false,
  window_start: 2,
  window_end: 5,
  dedup_refresh: false,
  series_prune: false,
  author_split: false,
  tombstone_cleanup: false,
  reconcile: false,
  purge_deleted: false,
  purge_old_logs: false,
  db_optimize: false,
  library_scan: false,
  library_organize: false,
  metadata_refresh: false,
  library_size_refresh: false,
  acoustid_online_lookup: false,
  acoustid_nightly_limit: 1000,
};

describe('MaintenanceSettingsSection', () => {
  it('renders the section heading', () => {
    const onChange = vi.fn();
    render(<MaintenanceSettingsSection config={defaultConfig} onChange={onChange} />);
    expect(screen.getByText('Maintenance Window')).toBeInTheDocument();
  });

  it('renders the enable nightly maintenance label', () => {
    const onChange = vi.fn();
    render(<MaintenanceSettingsSection config={defaultConfig} onChange={onChange} />);
    expect(screen.getByText('Enable nightly maintenance window')).toBeInTheDocument();
  });

  it('calls onChange with enabled: true when main Switch is toggled on', () => {
    const onChange = vi.fn();
    render(<MaintenanceSettingsSection config={defaultConfig} onChange={onChange} />);
    const switchInput = screen.getByRole('checkbox', {
      name: /enable nightly maintenance window/i,
    });
    fireEvent.click(switchInput);
    expect(onChange).toHaveBeenCalledWith({ enabled: true });
  });

  it('calls onChange with enabled: false when main Switch is toggled off', () => {
    const onChange = vi.fn();
    render(
      <MaintenanceSettingsSection
        config={{ ...defaultConfig, enabled: true }}
        onChange={onChange}
      />
    );
    const switchInput = screen.getByRole('checkbox', {
      name: /enable nightly maintenance window/i,
    });
    fireEvent.click(switchInput);
    expect(onChange).toHaveBeenCalledWith({ enabled: false });
  });

  it('renders a task toggle label (Dedup refresh)', () => {
    const onChange = vi.fn();
    render(<MaintenanceSettingsSection config={defaultConfig} onChange={onChange} />);
    expect(screen.getByText('Dedup refresh')).toBeInTheDocument();
  });

  it('calls onChange with dedup_refresh: true when task Switch is toggled', () => {
    const onChange = vi.fn();
    render(<MaintenanceSettingsSection config={defaultConfig} onChange={onChange} />);
    const switchInput = screen.getByRole('checkbox', { name: /dedup refresh/i });
    fireEvent.click(switchInput);
    expect(onChange).toHaveBeenCalledWith({ dedup_refresh: true });
  });

  it('calls onChange with numeric acoustid_nightly_limit when field changes', () => {
    const onChange = vi.fn();
    render(<MaintenanceSettingsSection config={defaultConfig} onChange={onChange} />);
    const field = screen.getByLabelText('AcoustID nightly limit');
    fireEvent.change(field, { target: { value: '500' } });
    expect(onChange).toHaveBeenCalledWith({ acoustid_nightly_limit: 500 });
    const callArg = onChange.mock.calls[0][0] as { acoustid_nightly_limit: unknown };
    expect(typeof callArg.acoustid_nightly_limit).toBe('number');
  });
});
