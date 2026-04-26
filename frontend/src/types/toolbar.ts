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

export interface ToolbarMode {
  rows: ToolbarButton[][]
}

export interface ToolbarConfig {
  normal: ToolbarMode
  claude: ToolbarMode
}
