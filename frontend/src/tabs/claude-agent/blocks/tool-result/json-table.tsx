/** JSON pretty-printer used by tool_result + tool_use fallbacks.
 *
 *  Today the implementation lives in helpers/format.ts (`safeStringify`
 *  + `formatToolInput`) and the renderer just calls them via a <pre>
 *  in body.tsx. This file groups the public renderer entry-point so
 *  per-kind callers can import from `tool-result/json-table` rather
 *  than reaching into helpers directly.
 */
import { safeStringify } from '../helpers/format'

import styles from '../../blocks.module.css'

export function JsonTable({ value }: { value: unknown }) {
  return <pre className={styles.toolPre}>{safeStringify(value)}</pre>
}
