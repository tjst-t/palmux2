// Minimal reconnecting WebSocket wrapper.
//
// Reconnect uses exponential backoff with jitter, capped at 30 s, matching
// the architecture doc's "1s → 30s" guidance. Subscribers get callbacks for
// every state transition so the terminal view can show / hide its
// "Reconnecting…" overlay.

export type ConnState = 'connecting' | 'open' | 'closing' | 'closed'

export interface ReconnectingWSOptions {
  url: string
  binaryType?: BinaryType
  initialDelayMs?: number
  maxDelayMs?: number
  protocols?: string | string[]
  onState?: (state: ConnState) => void
  onMessage?: (ev: MessageEvent) => void
  onError?: (ev: Event) => void
}

export class ReconnectingWebSocket {
  private opts: Required<Omit<ReconnectingWSOptions, 'protocols'>> & Pick<ReconnectingWSOptions, 'protocols'>
  private ws: WebSocket | null = null
  private state: ConnState = 'closed'
  private retryCount = 0
  private retryTimer: ReturnType<typeof setTimeout> | null = null
  private intentionallyClosed = false

  constructor(opts: ReconnectingWSOptions) {
    this.opts = {
      url: opts.url,
      binaryType: opts.binaryType ?? 'arraybuffer',
      initialDelayMs: opts.initialDelayMs ?? 1000,
      maxDelayMs: opts.maxDelayMs ?? 30000,
      protocols: opts.protocols,
      onState: opts.onState ?? (() => {}),
      onMessage: opts.onMessage ?? (() => {}),
      onError: opts.onError ?? (() => {}),
    }
  }

  connect(): void {
    this.intentionallyClosed = false
    this.openSocket()
  }

  send(data: string | ArrayBuffer | Blob): boolean {
    if (this.ws && this.ws.readyState === WebSocket.OPEN) {
      this.ws.send(data)
      return true
    }
    return false
  }

  close(code?: number, reason?: string): void {
    this.intentionallyClosed = true
    if (this.retryTimer) {
      clearTimeout(this.retryTimer)
      this.retryTimer = null
    }
    if (this.ws) {
      this.ws.close(code, reason)
    }
  }

  getState(): ConnState {
    return this.state
  }

  private setState(state: ConnState): void {
    if (this.state === state) return
    this.state = state
    this.opts.onState(state)
  }

  private openSocket(): void {
    this.setState('connecting')
    const ws = new WebSocket(this.opts.url, this.opts.protocols)
    ws.binaryType = this.opts.binaryType
    this.ws = ws
    ws.onopen = () => {
      this.retryCount = 0
      this.setState('open')
    }
    ws.onmessage = (ev) => this.opts.onMessage(ev)
    ws.onerror = (ev) => this.opts.onError(ev)
    ws.onclose = () => {
      this.setState('closed')
      this.ws = null
      if (!this.intentionallyClosed) {
        this.scheduleReconnect()
      }
    }
  }

  private scheduleReconnect(): void {
    const base = Math.min(this.opts.initialDelayMs * 2 ** this.retryCount, this.opts.maxDelayMs)
    const jitter = Math.random() * 0.3 * base
    const delay = base + jitter
    this.retryCount += 1
    this.retryTimer = setTimeout(() => {
      this.retryTimer = null
      this.openSocket()
    }, delay)
  }
}
