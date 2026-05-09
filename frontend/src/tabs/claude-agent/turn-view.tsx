/** TurnView — single-row renderer for the conversation list.
 *
 *  Renders one Turn (user / assistant / tool / hook). Plumbs
 *  permission / plan / ask handlers down into the per-block
 *  renderers and wires up sub-agent (Task) children inline.
 *
 *  Extracted from claude-agent-view.tsx in S43cfb1-2 to keep that
 *  file focused on hook composition + JSX top-level layout.
 */
import { BlockView } from './blocks'
import { UserTurnEditor } from './user-turn-editor'
import type {
  AskHandlersForView,
  PlanHandlersForView,
} from './hooks/use-permission-handlers'
import type { Turn } from './types'
import styles from './claude-agent-view.module.css'

export type RespondPermissionFn = (
  permissionId: string,
  decision: 'allow' | 'deny',
  scope: 'once' | 'session' | 'always',
  reason?: string,
  updatedInput?: unknown,
) => void

interface TurnViewProps {
  turn: Turn
  activeVersionIndex?: number
  onSetVersion?: (index: number) => void
  onRewind?: (turnId: string, newMessage: string) => Promise<void>
  onRewindApplyLocal?: (turnId: string, newContent: string) => void
  /** Lifted edit-state. The id of the user turn whose UserTurnEditor
   *  is currently in edit mode (or null when nothing is being edited).
   *  Lifted out of UserTurnEditor so the state survives row unmount
   *  caused by react-window when the row scrolls out of view. */
  editingTurnId?: string | null
  onEditingChange?: (turnId: string, editing: boolean) => void
  onRespondPermission: RespondPermissionFn
  planHandlersFor: (blockId: string | undefined) => PlanHandlersForView | undefined
  askHandlersFor: (blockId: string | undefined) => AskHandlersForView | undefined
  /** Map of toolUseId → child turns produced by sub-agents the CLI
   *  spawned via that Task tool block. When a block in this turn has
   *  a non-empty entry in this map, it is rendered as a TaskTree with
   *  the children inlined underneath. */
  childrenByParent?: Map<string, Turn[]>
}

export function TurnView({
  turn,
  activeVersionIndex,
  onSetVersion,
  onRewind,
  onRewindApplyLocal,
  editingTurnId,
  onEditingChange,
  onRespondPermission,
  planHandlersFor,
  askHandlersFor,
  childrenByParent,
}: TurnViewProps) {
  if (turn.role === 'user') {
    // S019: hand off to UserTurnEditor when the parent supplied
    // rewind handlers. Falls back to the simple bubble for callers
    // (e.g. printing-only views, sub-agent turns) that didn't pass
    // them through.
    if (onRewind && onRewindApplyLocal && onSetVersion) {
      return (
        <div className={styles.turnUser}>
          <UserTurnEditor
            turn={turn}
            activeVersionIndex={activeVersionIndex ?? -1}
            onSetVersion={onSetVersion}
            onRewind={onRewind}
            onRewindApplyLocal={onRewindApplyLocal}
            editing={editingTurnId === turn.id}
            onEditingChange={onEditingChange}
          />
        </div>
      )
    }
    return (
      <div className={styles.turnUser}>
        <div className={styles.userBubble}>
          {turn.blocks.map((b) => (
            <BlockView key={b.id} block={b} />
          ))}
        </div>
      </div>
    )
  }
  // tool / hook / assistant turns share the same prose-flow layout — hook
  // turns are visually similar enough to tool result groups that we
  // reuse the same chrome rather than introducing a third style. The
  // HookBlock itself is what gives the row its distinct identity.
  const cls =
    turn.role === 'tool' || turn.role === 'hook'
      ? styles.turnTool
      : styles.turnAssistant
  return (
    <div className={cls}>
      {turn.blocks.map((b) => {
        const handlers =
          b.kind === 'permission' && !b.decision && b.permissionId
            ? {
                onAllow: (scope: 'once' | 'session' | 'always', updatedInput?: unknown) =>
                  onRespondPermission(b.permissionId!, 'allow', scope, undefined, updatedInput),
                onDeny: (reason?: string) =>
                  onRespondPermission(b.permissionId!, 'deny', 'once', reason),
              }
            : undefined
        const planHandlers = b.kind === 'plan' ? planHandlersFor(b.id) : undefined
        const askHandlers = b.kind === 'ask' ? askHandlersFor(b.id) : undefined
        // Sub-agent child turns: only relevant for tool_use blocks
        // (today only `Task` spawns sub-agents, but the linkage is
        // generic so any future tool that emits sub-agents nests too).
        const childTurns =
          b.kind === 'tool_use' && b.toolUseId
            ? childrenByParent?.get(b.toolUseId)
            : undefined
        const renderTaskChildren =
          childTurns && childTurns.length > 0
            ? () =>
                childTurns.map((child) => (
                  <TurnView
                    key={child.id}
                    turn={child}
                    onRespondPermission={onRespondPermission}
                    planHandlersFor={planHandlersFor}
                    askHandlersFor={askHandlersFor}
                    childrenByParent={childrenByParent}
                  />
                ))
            : undefined
        return (
          <BlockView
            key={b.id}
            block={b}
            permissionHandlers={handlers}
            planHandlers={planHandlers}
            askHandlers={askHandlers}
            renderTaskChildren={renderTaskChildren}
          />
        )
      })}
    </div>
  )
}
