/** Blocks dispatcher — routes a Block to its per-kind renderer.
 *
 *  Each kind lives in its own file so adding a new kind is a 1-file
 *  change here + a 1-file PR for the renderer. Helper functions used
 *  across multiple kinds live in `helpers/`.
 *
 *  Public API:
 *    - BlockView: dispatcher component (default consumer entry point)
 *    - splitTextWithAttachments / uploadURLForPath: re-exported for
 *      user-turn-editor (parses image-attachment lines back out of
 *      composed text)
 *    - Turn: re-exported for downstream callers that build trees
 */
import { AskQuestionBlock, type AskHandlers } from './ask'
import { CompactBlock } from './compact'
import { HookBlock } from './hook'
import { PermissionBlock, type PermissionHandlers } from './permission'
import { PlanBlock, type PlanHandlers } from './plan'
import { TaskTreeBlock } from './task-tree'
import { TextBlock, ThinkingBlock } from './text'
import { TodoBlock } from './todo'
import { ToolUseBlock } from './tool-use'
import { ToolResultBlock } from './tool-result'

import type { Block, Turn } from '../types'

interface BlockProps {
  block: Block
  permissionHandlers?: PermissionHandlers
  planHandlers?: PlanHandlers
  askHandlers?: AskHandlers
  /** When the block is a `Task` tool_use that spawned a sub-agent, the
   *  caller passes in a render function that produces the nested child
   *  turn list. The Task block then expands into a tree (header on top,
   *  children indented underneath). Undefined ⇒ render the block flat
   *  as before. */
  renderTaskChildren?: () => React.ReactNode
}

export function BlockView({ block, permissionHandlers, planHandlers, askHandlers, renderTaskChildren }: BlockProps) {
  switch (block.kind) {
    case 'text':        return <TextBlock text={block.text ?? ''} blockId={block.id} />
    case 'thinking':    return <ThinkingBlock text={block.text ?? ''} blockId={block.id} />
    case 'tool_use':
      if (renderTaskChildren) {
        return <TaskTreeBlock block={block} renderChildren={renderTaskChildren} />
      }
      return <ToolUseBlock block={block} />
    case 'tool_result': return <ToolResultBlock block={block} />
    case 'todo':        return <TodoBlock block={block} />
    case 'permission':  return <PermissionBlock block={block} handlers={permissionHandlers} />
    case 'plan':        return <PlanBlock block={block} handlers={planHandlers} />
    case 'ask':         return <AskQuestionBlock block={block} handlers={askHandlers} />
    case 'hook':        return <HookBlock block={block} />
    case 'compact':     return <CompactBlock block={block} />
    default:            return null
  }
}

// S13b16a-3: removed re-exports of splitTextWithAttachments / uploadURLForPath
// — callers now import them directly from './blocks/helpers/format'. Keeps
// react-refresh/only-export-components happy.
export type { Turn }
