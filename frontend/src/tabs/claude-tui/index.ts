import { registerTab } from '../../lib/tab-registry'
import { ClaudeTuiTab } from './claude-tui-tab'

// claude-tui is a PTY-backed tab managed by internal/tab/claudetui.
// It sends / receives raw binary over WS (unlike the tmux-backed
// TerminalView which uses JSON-framed {type:"input"} messages).
// The backend type identifier is "claude-tui" (see claudetui.TabType).
registerTab({ type: 'claude-tui', component: ClaudeTuiTab })

export { ClaudeTuiTab }
