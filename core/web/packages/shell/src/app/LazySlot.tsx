// file: core/web/packages/shell/src/app/LazySlot.tsx
// version: 0.1.0
// guid: 9f7ae224-3625-408e-9d64-4af2e68c39d6
// last-edited: 2026-11-22

import { lazy, Suspense, type ComponentType } from 'react';
import type { Lazy } from '../types';
import { ShellErrorBoundary } from './ShellErrorBoundary';

const lazyCache = new WeakMap<Lazy, ComponentType>();
/** One React.lazy per contribution, so a re-render never remounts it. */
export function lazyOf(load: Lazy): ComponentType {
  let c = lazyCache.get(load);
  if (!c) {
    c = lazy(load);
    lazyCache.set(load, c);
  }
  return c;
}

/** A contribution's component, loaded on demand, inside its own boundary. */
export function LazySlot({ load, label }: { load: Lazy; label: string }) {
  const C = lazyOf(load);
  return (
    <ShellErrorBoundary label={label}>
      <Suspense fallback={null}>
        <C />
      </Suspense>
    </ShellErrorBoundary>
  );
}

/**
 * A contribution that takes props (a row action, a panel, a setup step), loaded
 * on demand inside its own boundary. One React.lazy per `load`, as lazyOf.
 */
export function LazyWithProps<P extends object>({ load, label, props }: { load: () => Promise<{ default: ComponentType<P> }>; label: string; props: P }) {
  const C = lazyOf(load as unknown as Lazy) as unknown as ComponentType<P>;
  return (
    <ShellErrorBoundary label={label}>
      <Suspense fallback={null}>
        <C {...props} />
      </Suspense>
    </ShellErrorBoundary>
  );
}
