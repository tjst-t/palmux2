import { useEffect } from 'react'
import { Route, Routes } from 'react-router-dom'

import { HomeRedirect } from './components/redirect'
import { MainLayout } from './components/main-layout'
import { useEventStream } from './hooks/use-event-stream'
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
    <Routes>
      <Route path="/" element={<HomeOrLayout />} />
      <Route path="/:repoId/:branchId/:tabId/*" element={<MainLayout />} />
    </Routes>
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
