/** usePermissionHandlers — wires Plan + Ask permission flows.
 *
 *  Encapsulates the permission round-trip state (planDecisions
 *  optimistic flips, planAuthority lookup, planHandlersFor /
 *  askHandlersFor factories) so the view doesn't have to manage 80
 *  lines of permission wiring inline.
 *
 *  Public output:
 *    - planAuthority: { blockId, permissionId } of the live plan
 *    - planHandlersFor(blockId): handlers for one PlanBlock instance
 *    - askHandlersFor(blockId): handlers for one AskQuestionBlock
 *      instance
 *    - planDecisions / setPlanDecisions: exposed in case the parent
 *      needs to reset on session swap
 */
import { useCallback, useMemo, useState } from 'react'

import type { Block, Turn } from '../types'

type Decided = 'approved' | 'rejected'

interface PermissionModesResp {
  modes: string[]
  default: string
  source: 'cli' | 'fallback'
}

interface SendBundle {
  planRespond: (
    permissionId: string,
    decision: 'approve' | 'reject',
    payload?: { targetMode: string; editedPlan: string },
  ) => void
  askRespond: (permissionId: string, answers: string[][]) => void
}

export interface PlanHandlersForView {
  decided?: Decided
  targetMode?: string
  canActOnPlan: boolean
  onApprove: (mode: string, editedPlan?: string) => void
  onReject: () => void
  modes: string[]
  defaultMode: string
}

export interface AskHandlersForView {
  canRespond: boolean
  onRespond: (answers: string[][]) => void
}

interface UsePermissionHandlersArgs {
  turns: Turn[]
  pendingPlanByBlock: Record<string, string>
  pendingAskByBlock: Record<string, string>
  modes: PermissionModesResp
  send: SendBundle
}

interface UsePermissionHandlersResult {
  planAuthority: { blockId: string; permissionId: string } | undefined
  planHandlersFor: (blockId: string | undefined) => PlanHandlersForView | undefined
  askHandlersFor: (blockId: string | undefined) => AskHandlersForView | undefined
  planDecisions: Record<string, { decided: Decided; targetMode?: string }>
  setPlanDecisions: React.Dispatch<
    React.SetStateAction<Record<string, { decided: Decided; targetMode?: string }>>
  >
}

export function usePermissionHandlers({
  turns,
  pendingPlanByBlock,
  pendingAskByBlock,
  modes,
  send,
}: UsePermissionHandlersArgs): UsePermissionHandlersResult {
  // planDecisions tracks the optimistic UI flip on click. The server
  // echoes plan.decided afterwards which makes the decision durable
  // (block.planDecision) — the optimistic state is only there to hide
  // the action row immediately on click while the WS round-trip
  // happens.
  const [planDecisions, setPlanDecisions] = useState<
    Record<string, { decided: Decided; targetMode?: string }>
  >({})

  // Resolve the active plan block — the most recent kind:"plan" block
  // that has a permission_id stamped (from plan.question) and is not
  // already decided.
  const planAuthority = useMemo(
    () => findActivePlan(turns, pendingPlanByBlock),
    [turns, pendingPlanByBlock],
  )

  const planHandlersFor = useCallback(
    (blockId: string | undefined): PlanHandlersForView | undefined => {
      if (!blockId) return undefined
      const optimistic = planDecisions[blockId]
      const isActive = blockId === planAuthority?.blockId
      const permissionId = planAuthority?.permissionId
      const blockFromTurns = findPlanBlockById(turns, blockId)
      const decisionFromBlock = blockFromTurns?.planDecision
      const targetModeFromBlock = blockFromTurns?.planTargetMode
      const decided = optimistic?.decided ?? decisionFromBlock
      const targetMode = optimistic?.targetMode ?? targetModeFromBlock
      return {
        decided,
        targetMode,
        canActOnPlan: isActive && !decided && !!permissionId,
        modes: modes.modes.filter((m) => m !== 'plan'),
        defaultMode: resolveDefaultMode(modes),
        onApprove: (mode: string, editedPlan?: string) => {
          if (!permissionId) return
          setPlanDecisions((prev) => ({
            ...prev,
            [blockId]: { decided: 'approved', targetMode: mode },
          }))
          send.planRespond(permissionId, 'approve', {
            targetMode: mode,
            editedPlan: editedPlan ?? '',
          })
        },
        onReject: () => {
          if (!permissionId) return
          setPlanDecisions((prev) => ({
            ...prev,
            [blockId]: { decided: 'rejected' },
          }))
          send.planRespond(permissionId, 'reject')
        },
      }
    },
    [turns, planDecisions, planAuthority, modes, send],
  )

  const askHandlersFor = useCallback(
    (blockId: string | undefined): AskHandlersForView | undefined => {
      if (!blockId) return undefined
      const entry = Object.entries(pendingAskByBlock).find(([, bid]) => bid === blockId)
      if (!entry) return { canRespond: false, onRespond: () => {} }
      const [permissionId] = entry
      return {
        canRespond: true,
        onRespond: (answers) => send.askRespond(permissionId, answers),
      }
    },
    [pendingAskByBlock, send],
  )

  return {
    planAuthority,
    planHandlersFor,
    askHandlersFor,
    planDecisions,
    setPlanDecisions,
  }
}

// resolveDefaultMode picks the dropdown's initial value: "auto" if
// the CLI advertises it, otherwise the CLI default (excluding
// "plan"), otherwise "auto" as a last resort.
function resolveDefaultMode(modes: PermissionModesResp): string {
  const supports = (m: string) => modes.modes.includes(m)
  if (supports('auto')) return 'auto'
  if (modes.default && modes.default !== 'plan') return modes.default
  return 'auto'
}

// findActivePlan walks the turns newest-first and returns the most
// recent kind:"plan" block that:
//   - has a permission_id stamped (= the backend has issued a
//     plan.question and is awaiting our reply), and
//   - has not yet been decided.
// That block — and only that block — is the one whose action row we
// enable. The map argument is `pendingPlanByBlock` from agent-state.
function findActivePlan(
  turns: Turn[],
  pendingPlanByBlock: Record<string, string>,
): { blockId: string; permissionId: string } | undefined {
  // Build a reverse index for O(1) blockId → permissionId lookup.
  const byBlock = new Map<string, string>()
  for (const [permId, blockId] of Object.entries(pendingPlanByBlock)) {
    byBlock.set(blockId, permId)
  }
  for (let i = turns.length - 1; i >= 0; i--) {
    const t = turns[i]
    for (let j = t.blocks.length - 1; j >= 0; j--) {
      const b = t.blocks[j]
      if (b.kind !== 'plan') continue
      if (b.planDecision) continue
      const permId = byBlock.get(b.id) ?? b.permissionId
      if (!permId) continue
      return { blockId: b.id, permissionId: permId }
    }
  }
  return undefined
}

// findPlanBlockById returns the kind:"plan" Block whose id matches, or
// undefined. Used to read planDecision/planTargetMode for the read-only
// post-decision label.
function findPlanBlockById(turns: Turn[], blockId: string): Block | undefined {
  for (const t of turns) {
    for (const b of t.blocks) {
      if (b.id === blockId && b.kind === 'plan') return b
    }
  }
  return undefined
}
