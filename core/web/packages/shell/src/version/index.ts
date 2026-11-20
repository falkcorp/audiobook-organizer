// file: core/web/packages/shell/src/version/index.ts
// version: 0.1.0
// guid: c1c98a11-9dc7-4ba8-886d-266dadf0d1d1
// last-edited: 2026-11-20

export { isVersionCompatible, parseVersion, type ParsedVersion } from './compare';
export { intersect } from './intersect';
export { reportDiagnostics } from './diagnostics';
export { loadServerModules, ModulesLoadError, type ModulesClient } from './modules';
export type {
  Diagnostic,
  DiagnosticKind,
  IntersectResult,
  ServerModule,
  ServerModuleUI,
  ServerModules,
} from './types';
