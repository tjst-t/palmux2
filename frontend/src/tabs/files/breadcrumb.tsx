import styles from './breadcrumb.module.css'

interface Props {
  path: string
  onNavigate: (path: string) => void
}

export function Breadcrumb({ path, onNavigate }: Props) {
  const parts = path === '' ? [] : path.split('/').filter(Boolean)
  return (
    <nav className={styles.crumbs}>
      <button className={styles.crumb} onClick={() => onNavigate('')}>
        📁 root
      </button>
      {parts.map((segment, idx) => {
        const target = parts.slice(0, idx + 1).join('/')
        return (
          <span key={target} className={styles.row}>
            <span className={styles.sep}>/</span>
            <button className={styles.crumb} onClick={() => onNavigate(target)}>
              {segment}
            </button>
          </span>
        )
      })}
    </nav>
  )
}
