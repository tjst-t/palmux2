// Minimal type declaration for @novnc/novnc (no official @types package).
// The package exports its main entry via the root exports field:
//   "exports": "./core/rfb.js"
// so we declare the module as '@novnc/novnc'.
//
// Only the subset used in browser-view.tsx is declared here.
declare module '@novnc/novnc' {
  interface RFBOptions {
    credentials?: { password?: string }
    /** WebSocket sub-protocols to request. */
    wsProtocols?: string[]
  }

  class RFB extends EventTarget {
    /** Scale the remote desktop to fit the container element. */
    scaleViewport: boolean
    /** Clip the remote desktop to the container element (no scroll). */
    clipViewport: boolean
    /** Resize the remote session to match the container element. */
    resizeSession: boolean
    /** When true (noVNC default), clicking the canvas focuses it. We disable it
     *  so our hidden capture input keeps keyboard focus. */
    focusOnClick: boolean

    constructor(target: HTMLElement, url: string, options?: RFBOptions)
    disconnect(): void

    /**
     * Send a single key event to the remote. Public API.
     * @param keysym X11 keysym (from getKeysym), or 0 / null for "no symbol".
     * @param code   DOM `KeyboardEvent.code`-style physical key string
     *               (from getKeycode), used for scancode lookup.
     * @param down   true = press, false = release. Omit to send press+release.
     */
    sendKey(keysym: number | null, code: string, down?: boolean): void

    /** Focus the underlying VNC canvas. */
    focus(options?: FocusOptions): void
    /** Blur the underlying VNC canvas. */
    blur(): void
  }

  export default RFB
}

// noVNC's keysym-mapping helpers. Exposed via a vite alias (see vite.config.ts)
// because the package's bare-string `exports` field blocks the real subpath
// `@novnc/novnc/core/input/util.js`.
declare module '@novnc/novnc/util' {
  /**
   * Derive the most reliable physical-key string (`KeyboardEvent.code`-style)
   * from a DOM keyboard event. Returns 'Unidentified' when nothing is usable.
   */
  export function getKeycode(evt: KeyboardEvent): string
  /**
   * Derive the X11 keysym from a DOM keyboard event using noVNC's own logic
   * (handles special keys, modifiers, locations). Returns null when the key
   * cannot be identified.
   */
  export function getKeysym(evt: KeyboardEvent): number | null
}
