// file: core/web/packages/shell/vite/index.d.mts
// version: 0.1.0
// guid: 7c2e9a40-6b15-4d83-a0f7-1d5b8e3c9f26
// last-edited: 2026-11-20

import type { UserConfig } from 'vite';

export interface MediacoreOptions {
  appId: string; // the app's ID, 03 §3.6's ^[a-z][a-z0-9]{1,15}$; written to <meta name="mediacore-app">
  themeStorageKey?: string; // default `${appId}.shell.themeMode`; ^[A-Za-z0-9._:-]{1,128}$
  defaultMode?: 'light' | 'dark'; // default 'dark'
}
export function mediacore(options: MediacoreOptions): UserConfig;

export interface MediacoreLibraryOptions {
  name: '@mediacore/ui-kit' | '@mediacore/shell'; // the package being built; 'shell' adds the core-version define
}
export function mediacoreLibrary(options: MediacoreLibraryOptions): UserConfig;

export const APP_ID_PATTERN: RegExp;
export const THEME_KEY_PATTERN: RegExp;
export const BUILD_ID_PATTERN: RegExp;
export const MODE_ATTRIBUTE: 'data-mui-color-scheme';
export const SINGLETONS: readonly string[];
export function themeBootScript(storageKey: string, defaultMode: 'light' | 'dark'): string;
export interface CleanCommit {
  shortSha: string;
  unixTime: number;
}
export function resolveBuildId(src: {
  env?: string;
  cleanCommit: () => CleanCommit | null;
  now: () => Date;
  randomHex: (n: number) => string;
}): string;
export function headTags(o: { buildId: string; appId: string; themeStorageKey: string; defaultMode: 'light' | 'dark' }): {
  tag: string;
  attrs?: Record<string, string>;
  children?: string;
  injectTo: 'head-prepend';
}[];
