// file: core/web/packages/ui-kit/src/theme/theme.contrast.test.ts
// version: 0.1.0
// guid: 9e5a1c38-2b74-4d06-a3f9-6d8c0e2b7f41
// last-edited: 2026-11-20

import { describe, expect, it } from 'vitest';
import { basePalettes, createMediaTheme } from './createMediaTheme';
import { AA_NORMAL, contrastFindings, contrastRatio } from './contrast';

// 07 §7.7 / §7.18: contrast in both schemes, measured on the theme the factory
// builds (not a copy of its constants), and the same check an app or module
// runs over its own extension.

describe('contrast helper', () => {
  it('measures the WCAG anchors', () => {
    expect(contrastRatio('#ffffff', '#000000')).toBeCloseTo(21, 1);
    expect(contrastRatio('#1976d2', '#1976d2')).toBeCloseTo(1, 5);
  });
});

describe('createMediaTheme contrast', () => {
  it('has no pair under AA in either scheme', () => {
    expect(contrastFindings(createMediaTheme())).toEqual([]);
  });

  it('measures the dark primary and secondary on both surfaces', () => {
    for (const fg of [basePalettes.dark.primary.main, basePalettes.dark.secondary.main]) {
      for (const bg of [basePalettes.dark.background.default, basePalettes.dark.background.paper]) {
        expect(contrastRatio(fg, bg)).toBeGreaterThanOrEqual(AA_NORMAL);
      }
    }
  });

  it('reports an extension that breaks contrast, in the scheme it breaks', () => {
    // A navy brand primary reads on the light surfaces and vanishes on the dark ones.
    const findings = contrastFindings(createMediaTheme({ brand: { primary: '#0d47a1' } }));
    expect(findings.length).toBeGreaterThan(0);
    expect(findings.every((f) => f.scheme === 'dark' && f.pair.startsWith('primary.main'))).toBe(true);
  });

  it('checks module extensions, applied after the app extension', () => {
    const theme = createMediaTheme({ extend: { colorSchemes: { dark: { palette: { secondary: { main: '#0000ee' } } } } } }, [
      { colorSchemes: { dark: { palette: { secondary: { main: '#f48fb1' } } } } },
    ]);
    const schemes = (theme as unknown as { colorSchemes: Record<string, { palette: { secondary: { main: string } } }> }).colorSchemes;
    expect(schemes.dark.palette.secondary.main).toBe('#f48fb1');
    expect(contrastFindings(theme)).toEqual([]);
  });

  it('derives the palette from an extension colour, not the base one', () => {
    const theme = createMediaTheme({}, [{ colorSchemes: { dark: { palette: { primary: { main: '#ffeb3b' } } } } }]);
    const dark = (theme as unknown as { colorSchemes: Record<string, { palette: { primary: { main: string; contrastText: string; dark: string } } }> })
      .colorSchemes.dark.palette.primary;
    expect(dark.main).toBe('#ffeb3b');
    // A yellow primary gets dark text; the base light blue's would have been dark too, so check `dark` as well.
    expect(dark.contrastText).toBe('rgba(0, 0, 0, 0.87)');
    expect(dark.dark).not.toBe(createMediaThemeDarkPrimaryDark());
  });

  it('defaults to dark and switches schemes by attribute', () => {
    const theme = createMediaTheme() as unknown as { defaultColorScheme: string; cssVariables?: unknown };
    expect(theme.defaultColorScheme).toBe('dark');
    expect((createMediaTheme({ defaultMode: 'light' }) as unknown as { defaultColorScheme: string }).defaultColorScheme).toBe('light');
  });
});

function createMediaThemeDarkPrimaryDark(): string {
  return (createMediaTheme() as unknown as { colorSchemes: Record<string, { palette: { primary: { dark: string } } }> }).colorSchemes.dark.palette.primary.dark;
}
