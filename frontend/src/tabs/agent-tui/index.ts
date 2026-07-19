import { registerTab } from '../../lib/tab-registry'
import { AgentTuiTab } from './agent-tui-tab'

// agent-tui is a PTY-backed tab, either claude's tui mode
// (internal/tab/agenttui, formerly claudetui) or a generic agent kind
// (internal/tab/agenttab, S2b5691-1: codex/opencode). It sends / receives
// raw binary over WS (unlike the tmux-backed TerminalView which uses
// JSON-framed {type:"input"} messages).
//
// S2b5691-2: registered under two type keys:
//   'claude-tui' — the legacy synthetic type used by claude's tui mode
//                  (tab-content.tsx claude_mode==='tui' branch, unchanged
//                  from before this Story — URL/WS paths and all
//                  claude-tui-* testids are byte-identical).
//   'agent-tui'  — the fallback renderer tab-content.tsx resolves to for any
//                  other registry-known agent kind (codex, opencode, a
//                  user-defined generic agent, …).
registerTab({ type: 'claude-tui', component: AgentTuiTab })
registerTab({ type: 'agent-tui', component: AgentTuiTab })

export { AgentTuiTab }
