// Sprint Dashboard tab module — registered into the global tab registry
// the same way Files / Git / Claude do. Mounted in main.tsx via
// `import './tabs/sprint'`.
//
// S022 — lazy-loaded. The Sprint tab pulls in Mermaid (~700KB) and the
// markdown parsers chunk; deferring those keeps the initial bundle small.
import { lazy } from 'react'

import { registerTab } from '../../lib/tab-registry'

const SprintView = lazy(() =>
  import('./sprint-view').then((m) => ({ default: m.SprintView })),
)

registerTab({ type: 'sprint', component: SprintView })

export { SprintView }
