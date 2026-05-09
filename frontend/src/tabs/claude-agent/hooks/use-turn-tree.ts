/** useTurnTree — derives the renderable turn tree from agent-state.
 *
 *  Pure transforms over Turn[] that the view consumes:
 *    - applyVersionView: substitute archived versions when the user
 *      has scrolled a previous turn back to a different version (S019)
 *    - splitTurnTree: separate top-level turns (virtualised rows) from
 *      sub-agent (Task) child turns that nest inside TaskTreeBlock
 *
 *  Returns the rendered tree + a flat list of top-level turn ids
 *  needed by useScrollRestore for anchor lookup.
 */
import { useMemo } from 'react'

import type { Turn } from '../types'

interface UseTurnTreeArgs {
  turns: Turn[]
  archivedTurnsById: Record<string, Turn>
  activeVersionByTurnId: Record<string, number>
}

export function useTurnTree({ turns, archivedTurnsById, activeVersionByTurnId }: UseTurnTreeArgs) {
  const { topLevelTurns, childrenByParent } = useMemo(() => {
    const turnsForDisplay = applyVersionView(turns, archivedTurnsById, activeVersionByTurnId)
    return splitTurnTree(turnsForDisplay)
  }, [turns, archivedTurnsById, activeVersionByTurnId])

  const turnIds = useMemo(
    () => topLevelTurns.map((t) => t.id),
    [topLevelTurns],
  )

  return { topLevelTurns, childrenByParent, turnIds }
}

// applyVersionView (S019) returns a virtual turn list that reflects
// the user's currently-selected version for each user turn. When all
// `activeVersionByTurnId` entries are `-1` (or missing), this is
// just the input. When a user turn has a non-active version selected,
// we replace its blocks[0].text with the archived version's content,
// drop the live tail (turns past it), and splice in the archived
// `subsequentTurnIds` (looked up from `archivedTurnsById`) instead.
//
// Idempotent / side-effect-free; does NOT mutate state.turns. Used
// only for rendering — `state.turns` remains the canonical "live"
// thread.
export function applyVersionView(
  turns: Turn[],
  archivedById: Record<string, Turn>,
  activeByTurnId: Record<string, number>,
): Turn[] {
  // Fast path: no version overrides, return the input verbatim.
  let needRewrite = false
  for (const k in activeByTurnId) {
    if (activeByTurnId[k] >= 0) {
      needRewrite = true
      break
    }
  }
  if (!needRewrite) return turns

  // Find the EARLIEST user turn whose version is non-active. That's
  // where the rewrite begins — every subsequent turn after it is
  // discarded and replaced by the archived continuation.
  let pivotIdx = -1
  let pivotVersion = -1
  for (let i = 0; i < turns.length; i++) {
    const t = turns[i]
    if (t.role !== 'user') continue
    const ver = activeByTurnId[t.id]
    if (ver === undefined || ver < 0) continue
    if (!t.versions || ver >= t.versions.length) continue
    pivotIdx = i
    pivotVersion = ver
    break
  }
  if (pivotIdx < 0) return turns

  const pivotTurn = turns[pivotIdx]
  const archivedVersion = pivotTurn.versions![pivotVersion]
  const out: Turn[] = turns.slice(0, pivotIdx)
  // Replace the user turn's text with the archived version's content
  // and clear its `versions[]` for the rendered copy (the editor uses
  // the original turn from state.turns to know how many versions exist).
  const rewrittenPivot: Turn = {
    ...pivotTurn,
    blocks:
      pivotTurn.blocks.length > 0
        ? [
            { ...pivotTurn.blocks[0], text: archivedVersion.content, done: true },
            ...pivotTurn.blocks.slice(1),
          ]
        : [],
  }
  out.push(rewrittenPivot)
  for (const id of archivedVersion.subsequentTurnIds) {
    const t = archivedById[id]
    if (t) out.push(t)
  }
  return out
}

// splitTurnTree partitions the flat turns list into top-level turns
// (the units of virtualisation) and a parent→children map for
// sub-agent (Task) turns. Children render inline inside TaskTreeBlock
// rather than as their own virtual rows because the parent Task
// block already owns the collapsing chrome and child transcripts are
// typically short — splitting them across rows would couple row
// heights in a way that defeats clean ResizeObserver measurement.
export function splitTurnTree(turns: Turn[]): {
  topLevelTurns: Turn[]
  childrenByParent: Map<string, Turn[]>
} {
  const childrenByParent = new Map<string, Turn[]>()
  const topLevelTurns: Turn[] = []
  for (const t of turns) {
    if (t.parentToolUseId) {
      const arr = childrenByParent.get(t.parentToolUseId) ?? []
      arr.push(t)
      childrenByParent.set(t.parentToolUseId, arr)
    } else {
      topLevelTurns.push(t)
    }
  }
  return { topLevelTurns, childrenByParent }
}
