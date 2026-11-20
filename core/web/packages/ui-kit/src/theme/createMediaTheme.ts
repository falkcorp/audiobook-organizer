// file: core/web/packages/ui-kit/src/theme/createMediaTheme.ts
// version: 0.1.0
// guid: 8c1f4e72-0a3b-4d96-b5e8-2f7d9a6c1b04
// last-edited: 2026-11-20

import { createTheme, type Theme, type ThemeOptions } from '@mui/material/styles';
import { MODE_ATTRIBUTE } from './boot';

declare module '@mui/material/styles' {
  interface CssThemeVariables {
    enabled: true;
  }
}

/** The app's theme input (07 §7.7). Module tokens come through `moduleExtends`. */
export interface MediaThemeInput {
  brand?: { primary?: string; secondary?: string };
  defaultMode?: 'light' | 'dark'; // default 'dark' (ABO today)
  extend?: ThemeOptions; // app tokens via MUI module augmentation
}

export { MODE_ATTRIBUTE };

/** The generic palettes, exported so contrast tests measure the declared source. */
export const basePalettes = {
  light: {
    // #1565c0, not MUI's #1976d2: the latter measures 4.22:1 on the light
    // background, under AA. An app keeps its own brand through `brand`.
    primary: { main: '#1565c0' },
    secondary: { main: '#dc004e' },
    background: { default: '#f5f5f5', paper: '#ffffff' },
  },
  dark: {
    primary: { main: '#90caf9' },
    secondary: { main: '#f48fb1' },
    background: { default: '#0a1929', paper: '#1e2a38' },
  },
} as const;

/**
 * The shell theme: MUI CSS variables keyed on `data-mui-color-scheme`, a light
 * and a dark scheme, then the app's `extend`, then each module's `themeExtend`
 * in the order given (the shell passes them in module ID order, FE-16).
 * `brand` replaces primary/secondary in both schemes.
 */
export function createMediaTheme(input: MediaThemeInput = {}, moduleExtends: readonly ThemeOptions[] = []): Theme {
  const brand = {
    ...(input.brand?.primary ? { primary: { main: input.brand.primary } } : {}),
    ...(input.brand?.secondary ? { secondary: { main: input.brand.secondary } } : {}),
  };
  const base: ThemeOptions = {
    cssVariables: { colorSchemeSelector: MODE_ATTRIBUTE },
    defaultColorScheme: input.defaultMode ?? 'dark',
    colorSchemes: {
      light: { palette: { ...basePalettes.light, ...brand } },
      dark: { palette: { ...basePalettes.dark, ...brand } },
    },
    shape: { borderRadius: 10 },
    typography: {
      fontFamily: ['-apple-system', 'BlinkMacSystemFont', '"Segoe UI"', 'Roboto', '"Helvetica Neue"', 'Arial', 'sans-serif'].join(','),
      button: { textTransform: 'none', fontWeight: 600, letterSpacing: 0 },
    },
    components: {
      // Dark mode paints an elevation overlay as a background image on every
      // Paper; disabling it keeps raised surfaces on the palette.
      MuiPaper: { styleOverrides: { root: { backgroundImage: 'none' } } },
      MuiButton: { defaultProps: { disableElevation: true } },
    },
  };
  // Merged before the one createTheme call, never passed as createTheme's
  // trailing arguments: those are merged into the built theme, so a
  // primary.main an extension sets would keep the base colour's derived
  // contrastText, light, dark and channel values.
  let options = base;
  for (const ext of [input.extend, ...moduleExtends]) {
    if (ext) options = mergeOptions(options, ext);
  }
  return createTheme(options);
}

function isPlainObject(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && Object.getPrototypeOf(v) === Object.prototype;
}

/** A deep merge of plain objects; arrays, functions and other values in `over` replace. */
function mergeOptions<T>(base: T, over: unknown): T {
  if (!isPlainObject(base) || !isPlainObject(over)) return over as T;
  const out: Record<string, unknown> = { ...base };
  for (const [k, v] of Object.entries(over)) {
    out[k] = k in out && isPlainObject(out[k]) && isPlainObject(v) ? mergeOptions(out[k], v) : v;
  }
  return out as T;
}
