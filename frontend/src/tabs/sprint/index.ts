// Sprint Dashboard tab module — registered into the global tab registry
// the same way Files / Git / Claude do. Mounted in main.tsx via
// `import './tabs/sprint'`.

import { registerTab } from '../../lib/tab-registry'

import { SprintView } from './sprint-view'

registerTab({ type: 'sprint', component: SprintView })

export { SprintView }
