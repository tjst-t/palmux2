import { useEffect } from 'react'
import { Route, Routes } from 'react-router-dom'

import { CommandPalette } from './components/command-palette/command-palette'
import { ConfirmDialogRenderer } from './components/context-menu/confirm-dialog'
import { ContextMenuRenderer } from './components/context-menu/context-menu'
import { PromptDialogRenderer } from './components/context-menu/prompt-dialog'
import { SelectDialogRenderer } from './components/context-menu/select-dialog'
import { HomeRedirect } from './components/redirect'
import { MainLayout } from './components/main-layout'
import { useEventStream } from './hooks/use-event-stream'
import { useVisualViewport } from './hooks/use-visual-viewport'
import { usePalmuxStore } from './stores/palmux-store'

function App() {
  const bootstrap = usePalmuxStore((s) => s.bootstrap)
  const error = usePalmuxStore((s) => s.error)
  const theme = usePalmuxStore((s) => s.deviceSettings.theme)

  useEffect(() => {
    void bootstrap()
  }, [bootstrap])

  useEffect(() => {
    document.documentElement.setAttribute('data-theme', theme)
  }, [theme])

  useEventStream()
  useVisualViewport()

  if (error) {
    return (
      <div style={{ padding: 24, color: 'var(--color-error)' }}>
        <p>Error talking to Palmux server: {error}</p>
        <p style={{ color: 'var(--color-fg-muted)' }}>
          Open <code>?</code> via <code>/auth?token=…</code> if you started the server with{' '}
          <code>--token</code>.
        </p>
      </div>
    )
  }

  return (
    <>
      <Routes>
        <Route path="/" element={<HomeOrLayout />} />
        <Route path="/:repoId/:branchId/:tabId/*" element={<MainLayout />} />
      </Routes>
      <ContextMenuRenderer />
      <ConfirmDialogRenderer />
      <PromptDialogRenderer />
      <SelectDialogRenderer />
      <CommandPalette />
    </>
  )
}

function HomeOrLayout() {
  const bootstrapped = usePalmuxStore((s) => s.bootstrapped)
  return (
    <>
      <HomeRedirect />
      {bootstrapped && <MainLayout />}
    </>
  )
}

export default App
