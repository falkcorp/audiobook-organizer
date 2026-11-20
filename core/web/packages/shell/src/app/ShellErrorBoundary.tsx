// file: core/web/packages/shell/src/app/ShellErrorBoundary.tsx
// version: 0.1.0
// guid: 0f5b3d82-7e14-4a69-b8c0-9d2a6e1f4c37
// last-edited: 2026-11-20

import { Component, type ErrorInfo, type ReactNode } from 'react';
import { Alert, Button } from '@mui/material';

interface Props {
  children: ReactNode;
  /** What failed, for the message: "the app", "this page". */
  label?: string;
}

/**
 * Renders the error a child threw instead of a blank page: the outermost
 * provider of createShell, and around every lazy page (07 §7.4.2).
 */
export class ShellErrorBoundary extends Component<Props, { error: Error | null }> {
  state = { error: null as Error | null };

  static getDerivedStateFromError(error: Error) {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error(`@mediacore/shell: ${this.props.label ?? 'a component'} failed`, error, info.componentStack);
  }

  render() {
    if (!this.state.error) return this.props.children;
    return (
      <Alert
        severity="error"
        sx={{ m: 2 }}
        action={<Button color="inherit" size="small" onClick={() => this.setState({ error: null })}>Retry</Button>}
      >
        {this.props.label ? `${this.props.label} failed: ` : ''}
        {this.state.error.message || String(this.state.error)}
      </Alert>
    );
  }
}
