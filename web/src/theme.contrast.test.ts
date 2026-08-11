// file: web/src/theme.contrast.test.ts
// version: 1.0.0
// guid: 7e3d1a95-4c62-4b08-9f71-6a2d5c0e8b34
// last-edited: 2026-08-11

import { describe, expect, it } from 'vitest';
import { createAppTheme } from './theme';

// Reported 2026-08-11: the library page buttons in dark mode "too closely match
// the existing" background. They did: primary.main was '#1976d2' in BOTH modes,
// which is MUI's LIGHT-mode blue, and the library toolbar is built from
// `variant="outlined"` buttons that draw their label in primary.main.
//
// This pins the fix as a MEASUREMENT rather than a matter of taste, so a future
// palette edit cannot quietly put it back under the floor.

/** sRGB relative luminance, per WCAG 2.1. */
function luminance(hex: string): number {
  const h = hex.replace('#', '');
  const channels = [0, 2, 4].map((i) => {
    const v = parseInt(h.slice(i, i + 2), 16) / 255;
    return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * channels[0] + 0.7152 * channels[1] + 0.0722 * channels[2];
}

/** WCAG contrast ratio between two hex colours, 1:1 .. 21:1. */
function contrast(a: string, b: string): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}

// WCAG AA for normal-size text. Button labels are normal-size text.
const AA_NORMAL = 4.5;

describe('dark theme contrast', () => {
  const dark = createAppTheme('dark');
  const bg = dark.palette.background.default;
  const paper = dark.palette.background.paper;

  it('sanity-checks the contrast helper against known values', () => {
    // Anchors the maths itself: white on black is the maximum 21:1, and a
    // colour against itself is 1:1. Without these, a broken helper could make
    // every assertion below pass.
    expect(contrast('#ffffff', '#000000')).toBeCloseTo(21, 1);
    expect(contrast('#1976d2', '#1976d2')).toBeCloseTo(1, 5);
  });

  it('primary is legible on both dark surfaces', () => {
    // The old hardcoded '#1976d2' measured 3.89:1 here and FAILED this.
    expect(contrast(dark.palette.primary.main, bg)).toBeGreaterThanOrEqual(AA_NORMAL);
    expect(contrast(dark.palette.primary.main, paper)).toBeGreaterThanOrEqual(AA_NORMAL);
  });

  it('secondary is legible on both dark surfaces', () => {
    // The old hardcoded '#dc004e' measured 3.47:1 here and FAILED this.
    expect(contrast(dark.palette.secondary.main, bg)).toBeGreaterThanOrEqual(AA_NORMAL);
    expect(contrast(dark.palette.secondary.main, paper)).toBeGreaterThanOrEqual(AA_NORMAL);
  });

  it('is a real improvement over the previous shared palette', () => {
    // Guards against a "fix" that merely nudges past the threshold.
    expect(contrast(dark.palette.primary.main, bg)).toBeGreaterThan(contrast('#1976d2', bg));
    expect(contrast(dark.palette.secondary.main, bg)).toBeGreaterThan(contrast('#dc004e', bg));
  });
});

describe('light theme contrast', () => {
  const light = createAppTheme('light');

  it('keeps the original brand colours', () => {
    // The dark-mode fix must not repaint light mode.
    expect(light.palette.primary.main).toBe('#1976d2');
    expect(light.palette.secondary.main).toBe('#dc004e');
  });
});
