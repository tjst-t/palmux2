// Ports tab module — registered into the global tab registry.
// See8bd4-3: incus-container workspace port publishing (Caddy subdomain).
// Imported via a side-effect import in main.tsx.
import { lazy } from 'react'

import { registerTab } from '../../lib/tab-registry'

const PortsView = lazy(() =>
  import('./ports-view').then((m) => ({ default: m.PortsView })),
)

registerTab({ type: 'ports', component: PortsView })

export { PortsView }
