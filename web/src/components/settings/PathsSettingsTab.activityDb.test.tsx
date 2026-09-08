// file: web/src/components/settings/PathsSettingsTab.activityDb.test.tsx
// version: 1.0.0
// guid: 6a3f1e70-84c2-4d19-9b05-3e7d2c8a4f61
// last-edited: 2026-09-07

import { render, screen, fireEvent } from '@testing-library/react';
import { describe, it, expect, vi } from 'vitest';
import type React from 'react';
import { PathsSettingsTab } from './PathsSettingsTab';

const RESOLVED = '/mnt/bigdata/books/audiobook-organizer/.activity/activity.sqlite';

function renderTab(overrides: Record<string, unknown> = {}) {
  const handleChange = vi.fn();
  const props = {
    settings: {
      libraryPath: '/mnt/bigdata/books/audiobook-organizer',
      activityDbPath: '',
      activityDbMoveOnChange: true,
    },
    setSettings: vi.fn(),
    libraryPathError: null,
    handleChange,
    handleBrowseLibraryPath: vi.fn(),
    importPaths: [],
    scanStatuses: {},
    handleViewScanErrors: vi.fn(),
    handleRequestCancelScan: vi.fn(),
    handleScanImportFolder: vi.fn(),
    handleRemoveImportFolder: vi.fn(),
    setAddFolderDialogOpen: vi.fn(),
    envLocked: [] as string[],
    activityDbResolvedPath: RESOLVED,
    ...overrides,
  };
  render(<PathsSettingsTab {...(props as unknown as React.ComponentProps<typeof PathsSettingsTab>)} />);
  return { handleChange };
}

const pathField = () => screen.getByLabelText(/Activity Database Path/i);
// MUI's Switch renders a checkbox input wired to the label by FormControlLabel,
// so it is reached by its label text rather than by an accessible role name.
const moveToggle = () =>
  screen.getByLabelText(/Move the existing database when this path changes/i);

describe('PathsSettingsTab — activity database', () => {
  it('shows the resolved default as the placeholder when no path is set', () => {
    renderTab();
    expect(pathField()).toHaveAttribute('placeholder', RESOLVED);
    // The default location is stated in words too, so an operator who never
    // clicks into the field still learns where the database actually lives.
    expect(screen.getByText(new RegExp(RESOLVED.replace(/[.]/g, '\\.')))).toBeInTheDocument();
  });

  it('is editable when the environment sets nothing', () => {
    const { handleChange } = renderTab();
    expect(pathField()).not.toBeDisabled();

    fireEvent.change(pathField(), { target: { value: '/srv/activity.sqlite' } });
    expect(handleChange).toHaveBeenCalledWith('activityDbPath', '/srv/activity.sqlite');
  });

  // The regression this whole env_locked mechanism exists for. Production pins
  // ACTIVITY_DB_PATH in its systemd unit; the field must refuse the edit rather
  // than accept a value that is silently overwritten on the next boot.
  it('disables the path field and says why when ACTIVITY_DB_PATH is set', () => {
    renderTab({ envLocked: ['activity_db_path'] });

    expect(pathField()).toBeDisabled();
    expect(screen.getByText(/ACTIVITY_DB_PATH/)).toBeInTheDocument();
    // The phrase appears in both the banner and the field's helper text; either
    // alone would leave the reason visible only to someone looking in one place.
    expect(screen.getAllByText(/set by the environment/i).length).toBeGreaterThan(0);
  });

  // The two settings lock independently: an operator may pin the location without
  // also freezing the move behaviour.
  it('leaves the move toggle usable when only the path is env-locked', () => {
    renderTab({ envLocked: ['activity_db_path'] });
    expect(moveToggle()).not.toBeDisabled();
  });

  it('disables the move toggle when ACTIVITY_DB_MOVE_ON_CHANGE is set', () => {
    renderTab({ envLocked: ['activity_db_move_on_change'] });
    expect(moveToggle()).toBeDisabled();
    expect(pathField()).not.toBeDisabled();
  });

  it('warns that history is left behind when moving is turned off', () => {
    renderTab({
      settings: {
        libraryPath: '/library',
        activityDbPath: '/srv/activity.sqlite',
        activityDbMoveOnChange: false,
      },
    });
    expect(screen.getByText(/stays at the old path/i)).toBeInTheDocument();
  });

  it('warns that the move is large and happens during startup when it is on', () => {
    renderTab();
    expect(screen.getByText(/tens of gigabytes/i)).toBeInTheDocument();
  });

  it('reports the toggle change so it can be saved', () => {
    const { handleChange } = renderTab();
    fireEvent.click(moveToggle());
    expect(handleChange).toHaveBeenCalledWith('activityDbMoveOnChange', false);
  });
});
