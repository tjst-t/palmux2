import { registerTab } from '../../lib/tab-registry'
import { GitView } from './git-view'

registerTab({ type: 'git', component: GitView })

export { GitView }
