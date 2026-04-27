import { registerTab } from '../../lib/tab-registry'
import { ClaudeAgentView } from './claude-agent-view'

registerTab({ type: 'claude', component: ClaudeAgentView })

export { ClaudeAgentView }
