// file: web/src/components/common/ServerFileBrowser.tsx
// version: 1.6.2
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890

// Moved to @mediacore/ui-kit (F9-05). The browser takes browse/include/exclude as props; this
// wrapper supplies the legacy /api/v1 filesystem calls until the shell
// adoption (F9-03) supplies core's.

import { ServerFileBrowser as UiKitServerFileBrowser, type ServerFileBrowserProps } from '@mediacore/ui-kit';
import * as api from '../../services/api';

type LegacyServerFileBrowserProps = Omit<ServerFileBrowserProps, 'browse' | 'include' | 'exclude'>;

export function ServerFileBrowser(props: LegacyServerFileBrowserProps) {
  return (
    <UiKitServerFileBrowser
      {...props}
      browse={api.browseFilesystem}
      include={api.includeFilesystemPath}
      exclude={(path) => api.excludeFilesystemPath(path)}
    />
  );
}
