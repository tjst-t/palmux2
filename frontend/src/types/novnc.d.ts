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

    constructor(target: HTMLElement, url: string, options?: RFBOptions)
    disconnect(): void
  }

  export default RFB
}
