// file: core/web/packages/api-client/src/operationInfo.ts
// version: 1.0.0
// guid: e0f83b90-329a-4501-b205-5e615e298f79
// last-edited: 2026-11-08

/**
 * One operation of an OpenAPI document, as the generator records it in
 * src/generated/operations.ts and the runtime reads it.
 */
export interface OperationInfo {
  /** Lower-case HTTP method. */
  readonly method: string;
  /** The OpenAPI path template, with curly braces. */
  readonly path: string;
  readonly operationId: string | null;
  /** `x-mediacore-permission: public`: the call is `skipAuthRedirect` (07 §7.9). */
  readonly public: boolean;
}

/** The set of public operations of a table, looked up by method and path template. */
export function publicOperationSet(table: readonly OperationInfo[]): ReadonlySet<string> {
  return new Set(table.filter((o) => o.public).map((o) => operationKey(o.method, o.path)));
}

export function operationKey(method: string, path: string): string {
  return `${method.toLowerCase()} ${path}`;
}
