// S13b16a-3: extracted from git-image-diff.tsx so that file only exports
// React components — required by react-refresh/only-export-components for
// stable HMR.

const IMAGE_EXTS = new Set([
  'png',
  'jpg',
  'jpeg',
  'gif',
  'webp',
  'avif',
  'bmp',
  'ico',
  'svg',
])

export function isImageFile(path: string): boolean {
  const dot = path.lastIndexOf('.')
  if (dot < 0) return false
  return IMAGE_EXTS.has(path.slice(dot + 1).toLowerCase())
}
