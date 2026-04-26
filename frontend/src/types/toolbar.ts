// Toolbar config schema. Stored under settings.toolbar in the global
// settings.json. The frontend deep-merges user values onto the built-in
// defaults so partial overrides work.

export type ModifierKey = 'ctrl' | 'alt' | 'shift' | 'meta'

export type ToolbarButton =
  | ModifierButton
  | KeyButton
  | CtrlKeyButton
  | ArrowButton
  | FontSizeButton
  | CommandButton
  | ImeButton
  | SpeechButton
  | PopupButton

export interface ModifierButton {
  type: 'modifier'
  modifier: ModifierKey
  label?: string
}

export interface KeyButton {
  type: 'key'
  /** Logical key name. The renderer maps this to bytes (Esc, Tab, Enter, …)
   *  or sends `text` verbatim if provided. */
  key: string
  label?: string
  /** Override: send this exact string as terminal input. */
  text?: string
}

export interface CtrlKeyButton {
  type: 'ctrl-key'
  key: string // e.g. "c", "l", "u"
  label?: string
}

export interface ArrowButton {
  type: 'arrow'
  direction: 'up' | 'down' | 'left' | 'right'
  label?: string
}

export interface FontSizeButton {
  type: 'fontsize'
  /** delta in px applied to deviceSettings.fontSize */
  delta: number
  label?: string
}

export interface CommandButton {
  type: 'command'
  label?: string
}

export interface ImeButton {
  type: 'ime'
  label?: string
}

export interface SpeechButton {
  type: 'speech'
  label?: string
  /** BCP-47 language tag passed to SpeechRecognition. Defaults to the
   *  browser's navigator.language at runtime. */
  lang?: string
}

/**
 * PopupButton is a "tap to send the primary key, swipe-up / long-press to
 * pick from alts". Useful on mobile where the toolbar is short on space.
 */
export interface PopupButton {
  type: 'popup'
  label?: string
  /** First entry is the primary key (sent on plain tap). The rest open in
   *  a popover via long-press / arrow-up. Each alternate is a regular
   *  KeyButton or CtrlKeyButton. */
  primary: KeyButton | CtrlKeyButton
  alternates: (KeyButton | CtrlKeyButton)[]
}

export interface ToolbarMode {
  rows: ToolbarButton[][]
}

export interface ToolbarConfig {
  normal: ToolbarMode
  claude: ToolbarMode
}
