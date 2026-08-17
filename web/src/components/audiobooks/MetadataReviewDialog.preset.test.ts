// file: web/src/components/audiobooks/MetadataReviewDialog.preset.test.ts
// version: 1.0.0
// guid: 9d3e5a71-4c62-4b18-8f0a-6e2b7c1d4a95
// last-edited: 2026-08-17

import { beforeEach, describe, expect, it } from 'vitest';

import {
  DEFAULT_CONFIDENCE,
  STRICT_PRESET,
  loadStrictPreset,
} from './MetadataReviewDialog';
import { STORAGE_KEYS } from '../../lib/storageKeys';

const KEY = STORAGE_KEYS.METADATA_REVIEW_STRICT_PRESET;

/**
 * The whole point of the preset is that it STICKS. Three filters were being
 * re-applied by hand on every dialog open; if the persistence regresses, the
 * switch silently becomes a per-session convenience again and the annoyance
 * comes back without anything failing loudly.
 */
describe('strict review preset', () => {
  beforeEach(() => window.localStorage.clear());

  it('is off when nothing was ever saved', () => {
    expect(loadStrictPreset()).toBe(false);
  });

  it('reads back as on once saved', () => {
    window.localStorage.setItem(KEY, 'true');
    expect(loadStrictPreset()).toBe(true);
  });

  it('treats an explicit false as off', () => {
    window.localStorage.setItem(KEY, 'false');
    expect(loadStrictPreset()).toBe(false);
  });

  it('treats a junk value as off rather than throwing', () => {
    window.localStorage.setItem(KEY, 'yes-please');
    expect(loadStrictPreset()).toBe(false);
  });

  it('carries the three settings the user asked for', () => {
    expect(STRICT_PRESET.hideSkipped).toBe(true);
    expect(STRICT_PRESET.hideMultiBook).toBe(true);
    expect(STRICT_PRESET.confidenceThreshold).toBe(190);
  });

  it('uses a threshold above 100, which the score scale allows', () => {
    // Candidate scores are sums that routinely exceed 100%, and the slider's
    // max is 300 — so 190 must not be clamped to a 0-100 range anywhere.
    expect(STRICT_PRESET.confidenceThreshold).toBeGreaterThan(100);
    expect(STRICT_PRESET.confidenceThreshold).toBeLessThanOrEqual(300);
  });

  it('keeps the off-state default distinct from the preset value', () => {
    expect(DEFAULT_CONFIDENCE).toBe(85);
    expect(DEFAULT_CONFIDENCE).not.toBe(STRICT_PRESET.confidenceThreshold);
  });
});
