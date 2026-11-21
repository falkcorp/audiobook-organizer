import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';
import { TERMINAL_TASK_STATES, type TaskRunState } from '../src/contracts';
import { TASK_RUN_STATES } from '../src/providers/taskRuns';

// core exports no TypeScript list and core/openapi/core.json has no paths yet,
// so the Go source is the oracle: this test parses tasks/client.go and fails
// the moment the shell's hand-typed vocabulary drifts from it.
const goSource = readFileSync(resolve(import.meta.dirname, '../../../../tasks/client.go'), 'utf8');

function goStates(): string[] {
  return [...goSource.matchAll(/^\tState\w+\s+RunState = "([a-z_]+)"/gm)].map((m) => m[1]!);
}

function goTerminal(): string[] {
  const body = /func \(s RunState\) Terminal\(\) bool \{([\s\S]*?)\n\}/.exec(goSource)![1]!;
  const cases = /case ([^:]+):/.exec(body)![1]!.split(',').map((c) => c.trim());
  const byConst = new Map([...goSource.matchAll(/^\t(State\w+)\s+RunState = "([a-z_]+)"/gm)].map((m) => [m[1]!, m[2]!]));
  return cases.map((c) => byConst.get(c)!);
}

describe('shell task states against core', () => {
  it('lists exactly the states tasks.RunState defines', () => {
    expect(goStates().length).toBeGreaterThan(0);
    expect([...TASK_RUN_STATES].sort()).toEqual(goStates().sort());
  });

  it('has exactly the terminal set tasks.RunState.Terminal has', () => {
    expect(goTerminal().length).toBeGreaterThan(0);
    expect([...TERMINAL_TASK_STATES].sort()).toEqual(goTerminal().sort());
  });

  it('types TaskRunState as the same union', () => {
    const all: readonly TaskRunState[] = TASK_RUN_STATES;
    expect(all).toHaveLength(12);
  });
});
