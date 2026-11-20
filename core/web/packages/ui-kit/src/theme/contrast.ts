// file: core/web/packages/ui-kit/src/theme/contrast.ts
// version: 0.1.0
// guid: 0d6b2f94-1e7c-4a35-9b08-c4f3e8a1d572
// last-edited: 2026-11-20

import type { Theme } from '@mui/material/styles';

/** sRGB relative luminance of a #rrggbb colour, per WCAG 2.1. */
export function luminance(hex: string): number {
  const h = hex.replace('#', '');
  if (!/^[0-9a-fA-F]{6}$/.test(h)) throw new Error(`not a #rrggbb colour: ${hex}`);
  const [r, g, b] = [0, 2, 4].map((i) => {
    const v = parseInt(h.slice(i, i + 2), 16) / 255;
    return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

/** WCAG contrast ratio between two #rrggbb colours, 1 .. 21. */
export function contrastRatio(a: string, b: string): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}

/** WCAG AA for normal-size text. */
export const AA_NORMAL = 4.5;

export interface ContrastFinding {
  scheme: 'light' | 'dark';
  pair: string;
  ratio: number;
}

/**
 * Every pair of a theme's two schemes that falls under AA: primary,
 * secondary and text.primary against background.default and paper. Apps and
 * modules run it over their own extension in their tests (07 §7.7).
 */
export function contrastFindings(theme: Theme): ContrastFinding[] {
  const schemes = (theme as unknown as { colorSchemes: Record<'light' | 'dark', { palette: Theme['palette'] }> }).colorSchemes;
  const out: ContrastFinding[] = [];
  for (const scheme of ['light', 'dark'] as const) {
    const p = schemes[scheme].palette;
    const fgs: [string, string][] = [
      ['primary.main', p.primary.main],
      ['secondary.main', p.secondary.main],
      ['text.primary', p.text.primary],
    ];
    const bgs: [string, string][] = [
      ['background.default', p.background.default],
      ['background.paper', p.background.paper],
    ];
    for (const [fn, fg] of fgs) {
      for (const [bn, bg] of bgs) {
        const ratio = contrastRatio(over(fg, bg), over(bg, '#ffffff'));
        if (ratio < AA_NORMAL) out.push({ scheme, pair: `${fn} on ${bn}`, ratio });
      }
    }
  }
  return out;
}

/** [r, g, b, a] of #rgb, #rrggbb or rgb()/rgba(). */
function parse(c: string): [number, number, number, number] {
  let m = /^#([0-9a-fA-F]{6})$/.exec(c);
  if (m) return [0, 2, 4].map((i) => parseInt(m![1].slice(i, i + 2), 16)).concat(1) as [number, number, number, number];
  m = /^#([0-9a-fA-F]{3})$/.exec(c);
  if (m) return [...m[1]].map((x) => parseInt(x + x, 16)).concat(1) as [number, number, number, number];
  const r = /^rgba?\(\s*(\d+)\s*,\s*(\d+)\s*,\s*(\d+)\s*(?:,\s*([\d.]+)\s*)?\)$/.exec(c);
  if (r) return [Number(r[1]), Number(r[2]), Number(r[3]), r[4] === undefined ? 1 : Number(r[4])];
  throw new Error(`unsupported colour ${c}`);
}

/** fg composited over an opaque bg, as #rrggbb: a translucent text colour is measured as painted. */
function over(fg: string, bg: string): string {
  const [fr, fgG, fb, a] = parse(fg);
  const [br, bgG, bb] = parse(bg);
  const mix = (f: number, b: number) => Math.round(f * a + b * (1 - a));
  return '#' + [mix(fr, br), mix(fgG, bgG), mix(fb, bb)].map((x) => x.toString(16).padStart(2, '0')).join('');
}
