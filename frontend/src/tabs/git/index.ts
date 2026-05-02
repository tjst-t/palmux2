// S022 — Git tab is lazy-loaded so the diff renderer / Monaco diff
// chunk and the rest of the git ops UI are not in the initial bundle.
import { lazy } from 'react'

import { registerTab } from '../../lib/tab-registry'

const GitView = lazy(() =>
  import('./git-view').then((m) => ({ default: m.GitView })),
)

registerTab({ type: 'git', component: GitView })

export { GitView }
