// file: core/web/packages/ui-kit/src/index.ts
// version: 0.1.0
// guid: 4f2c9a86-5d13-4e70-b8a9-7c1e0f3d6b52
// last-edited: 2026-11-20

// @mediacore/ui-kit: the theme factory and presentational pieces (07 §7.3.1,
// §7.7). It imports neither @mediacore/api-client nor @mediacore/shell; a piece
// that needs data takes it as a prop.

export { basePalettes, createMediaTheme, MODE_ATTRIBUTE, type MediaThemeInput } from './theme/createMediaTheme';
export { themeBootScript } from './theme/boot';
export { AA_NORMAL, contrastFindings, contrastRatio, luminance, type ContrastFinding } from './theme/contrast';
export { CHORD_TIMEOUT_MS, useKeyboardShortcuts, type GoShortcut } from './shortcuts/useKeyboardShortcuts';
export { BlockingPage, DataState, type BlockingPageProps, type DataStateProps, type DataStatus } from './feedback/DataState';
export * from './hooks';
export * from './components';
