// file: core/web/packages/api-client/src/index.ts
// version: 1.0.0
// guid: 0b4c3d2a-8587-4e24-971b-f148734742a6
// last-edited: 2026-11-08

// @mediacore/api-client: the fetch runtime and the client generated from core's
// OpenAPI document (07 §7.8, §7.5.2). The names below are the contract of 07 §7.5.2.

export {
  checkBuild,
  checkModules,
  getStaleReasons,
  onStaleBuild,
  readPageBuildId,
  type BuildCheckOptions,
  type ModulesResponse,
  type StaleReason,
} from './buildId';
export {
  createCoreClient,
  createModuleClient,
  SESSION_ENDING_CODES,
  type ClientRuntime,
  type CoreClient,
  type CoreClientOptions,
  type ModuleClient,
} from './client';
export {
  ApiAuthRedirectError,
  ApiTimeoutError,
  assertNotAuthRedirect,
  isAbortError,
  isAuthRedirectError,
} from './errors';
export { coreOperations } from './generated/operations';
export type { components, operations, paths } from './generated/core';
export type { OperationInfo } from './operationInfo';
export { decodeProblem, isProblemResponse, LEGACY_PROBLEM_CODE, type Problem } from './problem';
