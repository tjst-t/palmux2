// Browser tab module — S62374c-2
// Registers the Browser tab into the global tab registry.
// Imported via a side-effect import in main.tsx.
import { lazy } from 'react'

import { registerTab } from '../../lib/tab-registry'

const BrowserView = lazy(() =>
  import('./browser-view').then((m) => ({ default: m.BrowserView })),
)

registerTab({ type: 'browser', component: BrowserView })

export { BrowserView }
export { BrowserFullscreen } from './browser-view'
