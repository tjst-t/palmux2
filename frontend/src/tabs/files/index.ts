import { registerTab } from '../../lib/tab-registry'
import { FilesView } from './files-view'

registerTab({ type: 'files', component: FilesView })

export { FilesView }
