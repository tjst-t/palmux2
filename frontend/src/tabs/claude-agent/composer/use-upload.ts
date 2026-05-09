/** useUpload — unified upload pipeline for composer attachments.
 *
 *  Each call to addFiles creates a chip in `uploading` state, POSTs
 *  the file to the per-branch upload endpoint, and replaces the chip
 *  with the server-confirmed metadata. On failure the chip flips to
 *  `error` so the user can see why and retry.
 */
import { useCallback, useMemo, useState } from 'react'

// Attachment is one piece of context the user has added to their pending
// message. S008 generalised this to any file kind — the only branch in
// the rendering and submission paths is `kind === 'image'` (which gets
// a thumbnail and the `[image: ...]` injection) versus everything else
// (📄 chip, `@<abspath>` injection).
export interface Attachment {
  id: string
  /** Display name shown in the chip. Server returns the original
   *  filename in `originalName`; we fall back to the local File.name
   *  while the POST is pending. */
  name: string
  /** Absolute server-side filesystem path. Empty until the POST
   *  resolves. */
  path: string
  /** blob: URL for image previews; empty for non-image attachments. */
  previewUrl: string
  kind: 'image' | 'file'
  /** Resolved MIME from the server (filled after upload). Empty for
   *  pre-upload chips. */
  mime?: string
  status: 'uploading' | 'ready' | 'error'
  /** Set when status === 'error' so the chip can show a tooltip. */
  errorMessage?: string
}

let attachmentCounter = 0
function newAttachmentId(): string {
  attachmentCounter += 1
  return `a${attachmentCounter}-${Date.now().toString(36)}`
}

interface UseUploadArgs {
  repoId: string
  branchId: string
}

interface UseUploadResult {
  attachments: Attachment[]
  setAttachments: React.Dispatch<React.SetStateAction<Attachment[]>>
  addFiles: (files: File[] | FileList) => void
  removeAttachment: (id: string) => void
  clearAttachments: () => void
  isUploading: boolean
}

export function useUpload({ repoId, branchId }: UseUploadArgs): UseUploadResult {
  const [attachments, setAttachments] = useState<Attachment[]>([])

  const uploadFile = useCallback(async (file: File) => {
    const isImage = file.type.startsWith('image/') ||
      // Some browsers don't infer MIME for clipboard images: fall back
      // to the file extension. Keeps paste-of-PNG-from-screenshot
      // working when the OS gives us application/octet-stream.
      /\.(png|jpe?g|gif|webp|bmp|svg|avif|heic)$/i.test(file.name)
    const previewUrl = isImage ? URL.createObjectURL(file) : ''
    const tempId = newAttachmentId()
    setAttachments((prev) => [
      ...prev,
      {
        id: tempId,
        name: file.name || (isImage ? 'image' : 'file'),
        path: '',
        previewUrl,
        kind: isImage ? 'image' : 'file',
        status: 'uploading',
      },
    ])
    try {
      const fd = new FormData()
      fd.append('file', file)
      const url =
        `/api/repos/${encodeURIComponent(repoId)}/branches/${encodeURIComponent(branchId)}/upload`
      const res = await fetch(url, {
        method: 'POST',
        credentials: 'include',
        body: fd,
      })
      if (!res.ok) {
        const detail = await safeReadError(res)
        setAttachments((prev) =>
          prev.map((a) =>
            a.id === tempId
              ? { ...a, status: 'error' as const, errorMessage: detail || `HTTP ${res.status}` }
              : a,
          ),
        )
        return
      }
      const data = (await res.json()) as {
        path?: string
        name?: string
        originalName?: string
        mime?: string
        kind?: 'image' | 'file'
      }
      if (!data.path) {
        setAttachments((prev) =>
          prev.map((a) =>
            a.id === tempId ? { ...a, status: 'error' as const, errorMessage: 'no path' } : a,
          ),
        )
        return
      }
      setAttachments((prev) =>
        prev.map((a) =>
          a.id === tempId
            ? {
                ...a,
                path: data.path!,
                name: data.originalName || a.name,
                mime: data.mime,
                // Trust the server's classification when available — it
                // resolves the MIME from the multipart header, which is
                // more reliable than our file.type sniff for
                // clipboard-pasted blobs.
                kind: (data.kind as 'image' | 'file') || a.kind,
                status: 'ready' as const,
              }
            : a,
        ),
      )
    } catch (err) {
      const msg = err instanceof Error ? err.message : 'upload failed'
      setAttachments((prev) =>
        prev.map((a) =>
          a.id === tempId ? { ...a, status: 'error' as const, errorMessage: msg } : a,
        ),
      )
    }
  }, [repoId, branchId])

  const addFiles = useCallback((files: File[] | FileList) => {
    const arr = Array.isArray(files) ? files : Array.from(files)
    for (const f of arr) void uploadFile(f)
  }, [uploadFile])

  const removeAttachment = useCallback((id: string) => {
    setAttachments((prev) => {
      const dropped = prev.find((a) => a.id === id)
      if (dropped?.previewUrl.startsWith('blob:')) URL.revokeObjectURL(dropped.previewUrl)
      return prev.filter((a) => a.id !== id)
    })
  }, [])

  const clearAttachments = useCallback(() => {
    setAttachments((prev) => {
      for (const a of prev) {
        if (a.previewUrl.startsWith('blob:')) URL.revokeObjectURL(a.previewUrl)
      }
      return []
    })
  }, [])

  const isUploading = useMemo(
    () => attachments.some((a) => a.status === 'uploading'),
    [attachments],
  )

  return {
    attachments,
    setAttachments,
    addFiles,
    removeAttachment,
    clearAttachments,
    isUploading,
  }
}

// safeReadError extracts a message from a non-2xx response. Server
// returns `{error: string}` for upload failures; we tolerate empty
// bodies and fall back to the status text.
async function safeReadError(res: Response): Promise<string> {
  try {
    const text = await res.text()
    if (!text) return ''
    try {
      const data = JSON.parse(text) as { error?: string }
      if (typeof data.error === 'string') return data.error
    } catch {
      // not JSON
    }
    return text
  } catch {
    return ''
  }
}

// eventCarriesFile checks whether the dragged/dropped object is a file
// (vs. a text selection or an internal DOM drag). Browsers expose this
// on dataTransfer.types as a "Files" entry.
export function eventCarriesFile(e: React.DragEvent): boolean {
  const dt = e.dataTransfer
  if (!dt) return false
  if (dt.types) {
    for (let i = 0; i < dt.types.length; i++) {
      if (dt.types[i] === 'Files') return true
    }
  }
  return false
}
