// S022 — Files tab is lazy-loaded so Monaco editor (~3MB+) and the
// drawio viewer chunk are not part of the initial bundle. The registry
// stores a wrapper component that uses React.lazy under the hood.
import { lazy } from 'react'

import { registerTab } from '../../lib/tab-registry'

const FilesView = lazy(() =>
  import('./files-view').then((m) => ({ default: m.FilesView })),
)

registerTab({ type: 'files', component: FilesView })

export { FilesView }
