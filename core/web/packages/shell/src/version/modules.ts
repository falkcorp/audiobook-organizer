// file: core/web/packages/shell/src/version/modules.ts
// version: 0.1.0
// guid: fe0cfbf7-4ad3-4629-a7cf-58bb92386479
// last-edited: 2026-11-20

// The shell's /api/core/v1/modules loader: fetches the document and feeds the
// parsed body to the api-client's stale-build rule (07 §7.5.2, case 2).

import { checkBuild, checkModules, decodeProblem, type Problem } from '@mediacore/api-client';
import type { ServerModules } from './types';

export const MODULES_PATH = '/api/core/v1/modules';

/** A non-2xx /modules answer, carrying the decoded problem. */
export class ModulesLoadError extends Error {
  readonly problem: Problem;
  readonly status: number;
  constructor(problem: Problem, status: number) {
    super(`GET ${MODULES_PATH} failed: ${status} ${problem.title ?? ''}`.trim());
    this.name = 'ModulesLoadError';
    this.problem = problem;
    this.status = status;
  }
}

export type ModulesClient =
  | { runtime: { fetch: typeof fetch; baseUrl: string; buildId: string; shell?: boolean } }
  | { fetch: typeof fetch; baseUrl: string; buildId: string };

export async function loadServerModules(client: ModulesClient, signal?: AbortSignal): Promise<ServerModules> {
  const src = 'runtime' in client ? client.runtime : client;
  const { fetch: doFetch, baseUrl, buildId } = src;
  // The shell is the caller whatever the client says: case 2 is only ever the shell's.
  const opts = { buildId, shell: true };
  const response = await doFetch(`${baseUrl}${MODULES_PATH}`, {
    headers: { Accept: 'application/json' },
    credentials: 'same-origin',
    ...(signal ? { signal } : {}),
  });
  // This fetch bypasses the client's middleware, so the header cases are applied here.
  checkBuild(response, opts);
  if (!response.ok) throw new ModulesLoadError(await decodeProblem(response), response.status);
  const body = (await response.json()) as ServerModules;
  checkModules(body, response, opts);
  return body;
}
