/** TaskTreeBlock — wraps ToolUseBlock with a children panel housing
 *  the sub-agent's turn transcript.
 *
 *  Behaviour:
 *    - while the Task tool_use is still running (`!block.done`), the
 *      children panel is rendered expanded so the user watches the
 *      sub-agent's progress live;
 *    - once `block.done` flips true, the children collapse to a one-
 *      liner with a "show sub-agent transcript" toggle;
 *    - the regular ToolUseBlock chevron continues to govern the parent
 *      block's input panel (independent of the children panel).
 */
import { useEffect, useRef, useState } from 'react'

import { ToolUseBlock } from './tool-use'

import type { Block } from '../types'
import styles from '../blocks.module.css'

export function TaskTreeBlock({
  block,
  renderChildren,
}: {
  block: Block
  renderChildren: () => React.ReactNode
}) {
  // Default state: expanded while running, auto-collapsed once done.
  // We track the previous `done` value so we only auto-toggle on the
  // false→true transition — a user who manually re-expanded a finished
  // task on reload shouldn't have it slammed shut.
  const running = !block.done
  const [showChildren, setShowChildren] = useState(running)
  const prevDone = useRef(block.done)
  useEffect(() => {
    if (!prevDone.current && block.done) {
      // task just finished — auto-collapse the sub-agent transcript
      setShowChildren(false)
    }
    prevDone.current = block.done
  }, [block.done])
  return (
    <div className={styles.taskTree}>
      <ToolUseBlock block={block} />
      {!running && (
        <button
          type="button"
          className={styles.taskToggle}
          onClick={() => setShowChildren((v) => !v)}
          title={showChildren ? 'Hide sub-agent transcript' : 'Show sub-agent transcript'}
        >
          <span className={`${styles.chevron} ${showChildren ? styles.expanded : ''}`}>›</span>
          {showChildren ? 'Hide sub-agent transcript' : (
            <span className={styles.taskToggleSummary}>Sub-agent transcript</span>
          )}
        </button>
      )}
      {showChildren && (
        <div className={styles.taskChildren}>{renderChildren()}</div>
      )}
    </div>
  )
}
