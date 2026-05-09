/** TodoBlock — TodoWrite tool output rendering. */
import { parseTodos, todoClass, todoIcon } from './helpers/todo-parse'

import type { Block } from '../types'
import styles from '../blocks.module.css'

export function TodoBlock({ block }: { block: Block }) {
  const todos = parseTodos(block.todos)
  if (!todos || todos.length === 0) return null
  return (
    <div className={styles.todo}>
      <div className={styles.todoHeader}>Todo</div>
      <ul className={styles.todoList}>
        {todos.map((t, i) => (
          <li key={i} className={styles.todoItem}>
            <span className={`${styles.todoStatus} ${todoClass(t.status)}`.trim()}>
              {todoIcon(t.status)}
            </span>
            <span className={todoClass(t.status)}>{t.content || t.activeForm}</span>
          </li>
        ))}
      </ul>
    </div>
  )
}
