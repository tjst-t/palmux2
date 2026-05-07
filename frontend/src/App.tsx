import { useEffect } from 'react'
import { Route, Routes, useNavigate } from 'react-router-dom'

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
import { TestHarness } from './tabs/claude-agent/test-harness'
// S034: Global settings page.
import { NetworkSettingsPage } from './tabs/settings/settings-page'

function App() {
  const navigate = useNavigate()
  const bootstrap = usePalmuxStore((s) => s.bootstrap)
  const error = usePalmuxStore((s) => s.error)
  const theme = usePalmuxStore((s) => s.deviceSettings.theme)

  useEffect(() => {
    void bootstrap()
  }, [bootstrap])

  useEffect(() => {
    document.documentElement.setAttribute('data-theme', theme)
  }, [theme])

  // S034 hotfix: ⌘, / Ctrl+, opens Settings page globally.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key === ',') {
        const target = e.target as HTMLElement | null
        // Don't intercept when typing in an input / textarea.
        if (
          target &&
          (target.tagName === 'INPUT' ||
            target.tagName === 'TEXTAREA' ||
            target.isContentEditable)
        ) {
          return
        }
        e.preventDefault()
        navigate('/settings/network')
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [navigate])

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
        {/* S017: hidden test harness route. Drives the virtualised
            ConversationList + Read-preview surface from synthetic data
            so E2E doesn't need a live claude CLI. */}
        <Route path="/__test/claude" element={<TestHarness />} />
        {/* S034: Global settings pages */}
        <Route path="/settings/network" element={<NetworkSettingsPage />} />
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
