// file: core/web/packages/shell/src/version/coreVersion.ts
// version: 0.1.0
// guid: 8c3a5e19-0b74-4d26-9f81-6e2d4b7a0c53
// last-edited: 2026-11-20

// env.d.ts is an ambient script (no exports), which an import cannot name.
// eslint-disable-next-line @typescript-eslint/triple-slash-reference
/// <reference path="../env.d.ts" />

/**
 * The @mediacore/shell version both presets define (07 §7.10, FE7-4). The
 * one module that reads the build-time global: an app that compiles the
 * shell from source (the development condition) does not include env.d.ts,
 * and the reference above brings the declaration with this file.
 */
export const CORE_VERSION: string = MEDIACORE_CORE_VERSION;
