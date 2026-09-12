// file: web/src/utils/sanitizeSettingsImport.test.ts
// version: 1.0.0
// guid: 8a2d4f60-1b3e-4c7a-b95d-6e0f2a7c3d19
// last-edited: 2026-09-12

import { describe, it, expect } from 'vitest';
import { sanitizeSettingsImport } from './sanitizeSettingsImport';
import type * as api from '../services/api';

// Legacy settings files carry keys that are no longer on api.Config, so the
// fixtures go through an untyped record the way JSON.parse output does.
const load = (raw: Record<string, unknown>) =>
  sanitizeSettingsImport(raw as Partial<api.Config>);

describe('sanitizeSettingsImport', () => {
  it('folds flat auto_update_* keys into the nested auto_update object', () => {
    const cleaned = load({
      auto_update_enabled: true,
      auto_update_channel: 'beta',
      auto_update_check_minutes: '30',
      auto_update_window_start: 2,
      auto_update_window_end: '4',
    });
    expect(cleaned.auto_update).toEqual({
      enabled: true,
      channel: 'beta',
      check_minutes: 30,
      window_start: 2,
      window_end: 4,
    });
  });

  it('folds flat maintenance_window_* keys into the nested maintenance object', () => {
    const cleaned = load({
      maintenance_window_enabled: false,
      maintenance_window_start: '1',
      maintenance_window_end: 5,
    });
    expect(cleaned.maintenance).toEqual({ enabled: false, window_start: 1, window_end: 5 });
  });

  it('never sends the flat legacy keys themselves', () => {
    const cleaned = load({
      auto_update_enabled: true,
      maintenance_window_start: 3,
    }) as Record<string, unknown>;
    expect(cleaned).not.toHaveProperty('auto_update_enabled');
    expect(cleaned).not.toHaveProperty('maintenance_window_start');
  });

  it('keeps a nested object from the file and ignores the flat keys beside it', () => {
    const nested = { enabled: false, channel: 'stable', check_minutes: 60, window_start: 0, window_end: 6 };
    const cleaned = load({
      auto_update: nested,
      auto_update_enabled: true,
      auto_update_channel: 'beta',
    });
    expect(cleaned.auto_update).toEqual(nested);
  });

  it('drops non-numeric strings and omits the object when nothing usable is left', () => {
    const cleaned = load({
      maintenance_window_start: 'soon',
      maintenance_window_end: '',
    });
    expect(cleaned).not.toHaveProperty('maintenance');
  });

  it('drops keys the server has no setting for', () => {
    const cleaned = load({
      metadata_llm_scoring_enabled: true,
      env_locked: { root_dir: true },
      activity_db_path: '/elsewhere/activity.db',
      root_dir: '/library',
    }) as Record<string, unknown>;
    expect(cleaned).toEqual({ root_dir: '/library' });
  });
});
