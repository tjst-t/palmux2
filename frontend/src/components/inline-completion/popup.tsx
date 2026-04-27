import type { CompletionOption, InlineCompletionState } from './use-inline-completion'
import styles from './inline-completion.module.css'

interface Props {
  state: InlineCompletionState
  onPick: (opt: CompletionOption) => void
}

export function InlineCompletionPopup({ state, onPick }: Props) {
  if (!state.activeTrigger) return null
  return (
    <div className={styles.popup} role="listbox" aria-label={state.activeTrigger.name}>
      <div className={styles.head}>{state.activeTrigger.name}</div>
      {state.options.length === 0 ? (
        <div className={styles.empty}>{state.loading ? 'Loading…' : 'No matches.'}</div>
      ) : (
        <ul className={styles.list}>
          {state.options.map((opt, i) => (
            <li
              key={opt.id}
              className={`${styles.item} ${i === state.selected ? styles.active : ''}`.trim()}
              role="option"
              aria-selected={i === state.selected}
              onMouseDown={(e) => {
                // Prevent the textarea from losing focus before onClick fires.
                e.preventDefault()
                onPick(opt)
              }}
            >
              <span className={styles.itemTitle}>{opt.label}</span>
              {opt.detail && <span className={styles.itemDetail}>{opt.detail}</span>}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
