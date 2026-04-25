import styles from './App.module.css'

function App() {
  return (
    <div className={styles.shell}>
      <header className={styles.header}>
        <span className={styles.brand}>Palmux v2</span>
        <span className={styles.phase}>Phase 0 · Scaffold</span>
      </header>
      <main className={styles.main}>
        <p className={styles.muted}>
          Web ベースのターミナルクライアント。実装フェーズ進行中。
        </p>
      </main>
    </div>
  )
}

export default App
