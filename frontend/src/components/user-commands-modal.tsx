/**
 * UserCommandsModal — S032
 *
 * A Modal popup for managing user-defined palette commands (palette.userCommands
 * in ~/.config/palmux/settings.json).  Opened via ⌘K → "> manage user commands".
 *
 * Row layout matches prototype/user-commands-modal.html (approved with 2 rounds).
 */

import { useCallback, useEffect, useState } from 'react'

import { api } from '../lib/api'
import { usePalmuxStore, type GlobalSettings, type UserCommand } from '../stores/palmux-store'
import { Modal } from './modal'

import styles from './user-commands-modal.module.css'

interface Props {
  open: boolean
  onClose: () => void
}

/** Blank row used by the "+ Add" button. */
const blankRow = (): UserCommand => ({ name: '', target: 'bash', command: '' })

/** Stable fallback to avoid creating a new array reference on every render. */
const EMPTY_USER_COMMANDS: UserCommand[] = []

export function UserCommandsModal({ open, onClose }: Props) {
  const serverUserCommands = usePalmuxStore(
    (s) => s.globalSettings.palette?.userCommands ?? EMPTY_USER_COMMANDS,
  )

  // Local editable rows — reset to server state when opened.
  const [rows, setRows] = useState<UserCommand[]>([])
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [dirty, setDirty] = useState(false)

  // Initialise / reset to server state when modal opens.
  useEffect(() => {
    if (open) {
      setRows(serverUserCommands.map((c) => ({ ...c })))
      setSaveError(null)
      setDirty(false)
    }
  }, [open, serverUserCommands])

  const markDirty = useCallback(() => setDirty(true), [])

  const updateRow = useCallback(
    (idx: number, field: keyof UserCommand, value: string) => {
      setRows((prev) => {
        const next = prev.map((r, i) => (i === idx ? { ...r, [field]: value } : r))
        // When target changes, clear the old payload field and set the new one.
        if (field === 'target') {
          const r = next[idx]
          next[idx] = { name: r.name, target: r.target as UserCommand['target'], notes: r.notes }
          if (r.target === 'bash') next[idx].command = ''
          if (r.target === 'url') next[idx].url = ''
          if (r.target === 'files') next[idx].path = ''
        }
        return next
      })
      markDirty()
    },
    [markDirty],
  )

  const addRow = useCallback(() => {
    setRows((prev) => [...prev, blankRow()])
    markDirty()
  }, [markDirty])

  const removeRow = useCallback(
    (idx: number) => {
      setRows((prev) => prev.filter((_, i) => i !== idx))
      markDirty()
    },
    [markDirty],
  )

  const reset = useCallback(() => {
    setRows(serverUserCommands.map((c) => ({ ...c })))
    setSaveError(null)
    setDirty(false)
  }, [serverUserCommands])

  const save = useCallback(async () => {
    setSaving(true)
    setSaveError(null)
    try {
      const updated = await api.patch<GlobalSettings>('/api/settings', {
        palette: { userCommands: rows },
      })
      // Merge updated settings back into the store so palette items refresh.
      // Call setState directly (not via a component-scope variable) to avoid
      // React's hook-call detection flagging it as an invalid hook during render.
      usePalmuxStore.setState((state) => ({ globalSettings: { ...state.globalSettings, ...updated } }))
      setDirty(false)
      onClose()
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err)
      setSaveError(msg)
    } finally {
      setSaving(false)
    }
  }, [rows, onClose])

  const payloadLabel = (target: UserCommand['target']) => {
    if (target === 'bash') return 'Command'
    if (target === 'url') return 'URL'
    return 'Path'
  }

  const payloadValue = (row: UserCommand) => {
    if (row.target === 'bash') return row.command ?? ''
    if (row.target === 'url') return row.url ?? ''
    return row.path ?? ''
  }

  const payloadField = (row: UserCommand): keyof UserCommand => {
    if (row.target === 'bash') return 'command'
    if (row.target === 'url') return 'url'
    return 'path'
  }

  const rawJSON = JSON.stringify({ palette: { userCommands: rows } }, null, 2)

  return (
    <Modal open={open} onClose={onClose} title="Manage user commands" width={820}>
      <div data-testid="user-commands-modal">
        <p className={styles.subtitle}>
          Custom entries that appear in{' '}
          <span className={styles.kbd}>⌘K</span>{' '}
          <code>&gt;</code> command mode. Saved to{' '}
          <code>~/.config/palmux/settings.json</code>.
        </p>

        {rows.length > 0 && (
          <div className={styles.headerRow}>
            <span>Name</span>
            <span>Command / URL / Path</span>
            <span>Notes</span>
            <span>Target</span>
            <span />
          </div>
        )}

        {rows.map((row, idx) => (
          <div
            key={idx}
            className={styles.cmdRow}
            data-testid={`user-cmd-row-${idx}`}
          >
            <input
              className={styles.input}
              type="text"
              placeholder="Name"
              value={row.name}
              aria-label="Command name"
              onChange={(e) => updateRow(idx, 'name', e.target.value)}
              data-testid={`user-cmd-name-${idx}`}
            />
            <input
              className={styles.input}
              type="text"
              placeholder={payloadLabel(row.target)}
              value={payloadValue(row)}
              aria-label={payloadLabel(row.target)}
              onChange={(e) => updateRow(idx, payloadField(row), e.target.value)}
              data-testid={`user-cmd-payload-${idx}`}
            />
            <input
              className={styles.input}
              type="text"
              placeholder="(optional notes)"
              value={row.notes ?? ''}
              aria-label="Notes"
              onChange={(e) => updateRow(idx, 'notes', e.target.value)}
              data-testid={`user-cmd-notes-${idx}`}
            />
            <select
              className={styles.targetSelect}
              value={row.target}
              aria-label="Target"
              onChange={(e) => updateRow(idx, 'target', e.target.value)}
              data-testid={`user-cmd-target-${idx}`}
            >
              <option value="bash">bash</option>
              <option value="url">url</option>
              <option value="files">files</option>
            </select>
            <button
              className={styles.removeRowBtn}
              title="Remove"
              aria-label="Remove row"
              onClick={() => removeRow(idx)}
              data-testid={`user-cmd-remove-${idx}`}
            >
              ✕
            </button>
          </div>
        ))}

        <button
          className={styles.addRowBtn}
          onClick={addRow}
          data-testid="user-cmd-add"
        >
          + Add user command
        </button>

        <details className={styles.rawDetails}>
          <summary className={styles.rawSummary}>
            View raw JSON (settings.json snippet)
          </summary>
          <pre className={styles.rawPre} data-testid="user-cmd-raw-json">
            {rawJSON}
          </pre>
        </details>

        <div className={styles.footer}>
          <span className={styles.footerMeta}>
            {rows.length} command{rows.length !== 1 ? 's' : ''}
            {dirty && <span className={styles.unsaved}> · unsaved changes</span>}
          </span>
          {saveError && (
            <span className={styles.saveError} data-testid="user-cmd-save-error">
              {saveError}
            </span>
          )}
          <button
            className={styles.btnGhost}
            onClick={reset}
            disabled={saving}
            data-testid="user-cmd-reset"
          >
            Reset
          </button>
          <button
            className={styles.btnPrimary}
            onClick={save}
            disabled={saving}
            data-testid="user-commands-save"
          >
            {saving ? 'Saving…' : 'Save'}
          </button>
        </div>
      </div>
    </Modal>
  )
}
