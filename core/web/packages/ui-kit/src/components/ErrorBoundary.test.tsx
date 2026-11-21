// file: core/web/packages/ui-kit/src/components/ErrorBoundary.test.tsx
// version: 0.1.0
// guid: 61e08792-7369-493f-9d26-5d97bb7f431e
// last-edited: 2026-11-21

import '@testing-library/jest-dom/vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ErrorBoundary } from './ErrorBoundary';

function Boom({ message }: { message: string }): never {
  throw new Error(message);
}

const realLocation = window.location;
afterEach(() => {
  Object.defineProperty(window, 'location', { value: realLocation, configurable: true });
  vi.restoreAllMocks();
});

function silenceReactErrorLog() {
  return vi.spyOn(console, 'error').mockImplementation(() => {});
}

describe('ErrorBoundary', () => {
  it('renders its children when nothing throws', () => {
    render(
      <ErrorBoundary>
        <p>Batcave online</p>
      </ErrorBoundary>,
    );
    expect(screen.getByText('Batcave online')).toBeInTheDocument();
  });

  it('shows the error message in place of a child that throws, and logs it', () => {
    const log = silenceReactErrorLog();
    render(
      <ErrorBoundary>
        <Boom message="The Joker got in" />
      </ErrorBoundary>,
    );
    expect(screen.getByText('Something went wrong')).toBeInTheDocument();
    expect(screen.getByText('The Joker got in')).toBeInTheDocument();
    expect(log.mock.calls.some((c) => c[0] === 'ErrorBoundary caught an error:')).toBe(true);
  });

  it('falls back to a generic line when the error has no message', () => {
    silenceReactErrorLog();
    render(
      <ErrorBoundary>
        <Boom message="" />
      </ErrorBoundary>,
    );
    expect(screen.getByText('An unexpected error occurred')).toBeInTheDocument();
  });

  function stubLocation() {
    const loc = { href: 'http://localhost/deep/page' } as Location;
    Object.defineProperty(window, 'location', { value: loc, configurable: true });
    return loc;
  }

  it('returns to / with the default label', () => {
    silenceReactErrorLog();
    const loc = stubLocation();
    render(
      <ErrorBoundary>
        <Boom message="x" />
      </ErrorBoundary>,
    );
    fireEvent.click(screen.getByText('Return to Dashboard'));
    expect(loc.href).toBe('/');
  });

  it('takes the destination and the label from props', () => {
    silenceReactErrorLog();
    const loc = stubLocation();
    render(
      <ErrorBoundary returnHref="/app/" returnLabel="Back to the Batcave">
        <Boom message="x" />
      </ErrorBoundary>,
    );
    fireEvent.click(screen.getByText('Back to the Batcave'));
    expect(loc.href).toBe('/app/');
  });
});
