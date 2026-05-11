/** ComposerSelectors — Model / Effort / PermissionMode PillSelect group.
 *
 *  The composer has three tightly-grouped pill selectors. Each
 *  carries optimistic local state (the parent passes the
 *  server-confirmed value via prop, but model.set / effort.set /
 *  permission_mode.set go out fire-and-forget — without local
 *  mirroring the selector visually snaps back to the old value the
 *  moment the user picks one).
 */
import { useEffect, useState } from 'react'

import { PillSelect, type PillSelectOption } from '../../../components/pill-select'

import type { ModelDescriptor } from '../types'

export const FALLBACK_MODELS: ModelDescriptor[] = [
  { value: '', displayName: 'default' },
  { value: 'sonnet', displayName: 'sonnet' },
  { value: 'opus', displayName: 'opus' },
  { value: 'haiku', displayName: 'haiku' },
]

export function modeLabel(mode: string): string {
  switch (mode) {
    case 'default':           return 'default'
    case 'acceptEdits':       return 'accept edits'
    case 'plan':              return 'plan'
    case 'auto':              return 'auto'
    case 'dontAsk':           return "don't ask"
    case 'bypassPermissions': return 'bypass'
    default:                  return mode
  }
}

interface ComposerSelectorsProps {
  models: ModelDescriptor[]
  model: string
  effort: string
  permissionMode: string
  permissionModes: string[]
  onModelChange: (m: string) => void
  onEffortChange: (e: string) => void
  onPermissionModeChange: (m: string) => void
}

export function ComposerSelectors({
  models,
  model,
  effort,
  permissionMode,
  permissionModes,
  onModelChange,
  onEffortChange,
  onPermissionModeChange,
}: ComposerSelectorsProps) {
  const [localModel, setLocalModel] = useState(model)
  const [localEffort, setLocalEffort] = useState(effort)
  const [localPermissionMode, setLocalPermissionMode] = useState(permissionMode)
  // eslint-disable-next-line react-hooks/set-state-in-effect -- prop-driven state sync (React 19 idiomatic exception)
  useEffect(() => setLocalModel(model), [model])
  // eslint-disable-next-line react-hooks/set-state-in-effect -- prop-driven state sync (React 19 idiomatic exception)
  useEffect(() => setLocalEffort(effort), [effort])
  // eslint-disable-next-line react-hooks/set-state-in-effect -- prop-driven state sync (React 19 idiomatic exception)
  useEffect(() => setLocalPermissionMode(permissionMode), [permissionMode])

  const handleModelChange = (m: string) => {
    setLocalModel(m)
    onModelChange(m)
  }
  const handleEffortChange = (e: string) => {
    setLocalEffort(e)
    onEffortChange(e)
  }
  const handlePermissionModeChange = (m: string) => {
    setLocalPermissionMode(m)
    onPermissionModeChange(m)
  }

  // Effort visibility derives from the currently-selected model's
  // descriptor. CLI returns supportedEffortLevels per model, plus a
  // supportsEffort flag — both must be set for the dropdown to show.
  const currentModelDescriptor = models.find((m) => m.value === localModel)
  const effortLevels = currentModelDescriptor?.supportedEffortLevels ?? []
  const showEffort = !!currentModelDescriptor?.supportsEffort && effortLevels.length > 0

  return (
    <>
      <PillSelect
        ariaLabel="Model"
        value={localModel}
        onChange={handleModelChange}
        options={models.map<PillSelectOption>((m) => ({
          value: m.value,
          label: m.displayName ?? m.value ?? 'default',
          detail: m.description,
        }))}
      />
      {showEffort && (
        <PillSelect
          ariaLabel="Effort"
          prefix="effort"
          value={localEffort}
          onChange={handleEffortChange}
          options={[
            { value: '', label: 'default' },
            ...effortLevels.map<PillSelectOption>((lvl) => ({ value: lvl, label: lvl })),
          ]}
        />
      )}
      <PillSelect
        ariaLabel="Permission mode"
        value={localPermissionMode}
        onChange={handlePermissionModeChange}
        options={permissionModes.map<PillSelectOption>((m) => ({
          value: m,
          label: modeLabel(m),
        }))}
      />
    </>
  )
}
