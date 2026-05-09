/** PermissionBlock — Bash / generic tool permission prompt. */
import { useState } from 'react'

import { safeStringify } from './helpers/format'

import type { Block } from '../types'
import styles from '../blocks.module.css'

export interface PermissionHandlers {
  onAllow: (scope: 'once' | 'session' | 'always', updatedInput?: unknown) => void
  onDeny: (reason?: string) => void
}

export function PermissionBlock({ block, handlers }: { block: Block; handlers?: PermissionHandlers }) {
  const inputStr = block.input == null ? '' : safeStringify(block.input)
  const decided = !!block.decision
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState('')
  const [editError, setEditError] = useState<string | null>(null)

  const startEdit = () => {
    setDraft(inputStr)
    setEditError(null)
    setEditing(true)
  }

  const submitEdit = () => {
    if (!handlers) return
    let parsed: unknown
    try {
      parsed = JSON.parse(draft)
    } catch (e) {
      setEditError(e instanceof Error ? e.message : String(e))
      return
    }
    handlers.onAllow('once', parsed)
    setEditing(false)
  }

  return (
    <div className={styles.permission}>
      <div className={styles.permissionHeader}>
        <span>Tool permission requested:</span>
        <span className={styles.permissionToolName}>{block.toolName}</span>
      </div>
      {!editing && inputStr && <div className={styles.permissionInput}>{inputStr}</div>}
      {editing && (
        <div className={styles.permissionEdit}>
          <textarea
            className={styles.permissionEditArea}
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            spellCheck={false}
            rows={Math.min(12, draft.split('\n').length + 1)}
          />
          {editError && <div className={styles.permissionEditError}>JSON parse error: {editError}</div>}
        </div>
      )}
      {decided ? (
        <div className={styles.permissionDecision}>Decision: {block.decision}</div>
      ) : handlers ? (
        editing ? (
          <div className={styles.permissionActions}>
            <button className={styles.allow} onClick={submitEdit}>
              Allow with edits
            </button>
            <button onClick={() => setEditing(false)}>Cancel</button>
          </div>
        ) : (
          <div className={styles.permissionActions}>
            <button className={styles.allow} onClick={() => handlers.onAllow('once')}>
              Allow (y)
            </button>
            <button onClick={() => handlers.onAllow('session')}>Allow for session</button>
            <button
              onClick={() => handlers.onAllow('always')}
              title="Add this tool to .claude/settings.json permissions.allow"
            >
              Always allow
            </button>
            <button onClick={startEdit}>Edit…</button>
            <button className={styles.deny} onClick={() => handlers.onDeny()}>
              Deny (n)
            </button>
          </div>
        )
      ) : null}
    </div>
  )
}
