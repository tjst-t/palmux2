/** Diff renderer used by ToolInputRich for Edit / Write tool inputs.
 *
 *  This file is a thin facade over the shared DiffView component. It
 *  exists so each "kind" of tool_result rendering has a dedicated
 *  module per the S43cfb1-1 directory structure: index (dispatcher) /
 *  body (output dispatcher) / diff / json-table.
 *
 *  Today the diff path is reached from ToolInputRich (Edit / Write
 *  tool_use blocks) rather than ToolResultBody, but conceptually a
 *  diff IS the result of an Edit tool — keeping the renderer here
 *  groups tool_result presentation logic in one directory.
 */
import { DiffView, buildSyntheticDiff } from '../../../../components/diff/diff-view'

export function SyntheticDiff({
  filePath,
  oldStr,
  newStr,
}: {
  filePath: string
  oldStr: string
  newStr: string
}) {
  const file = buildSyntheticDiff(filePath, oldStr, newStr)
  return <DiffView files={[file]} />
}
