import type { ToolbarConfig } from '../types/toolbar'

export const DEFAULT_TOOLBAR_CONFIG: ToolbarConfig = {
  normal: {
    rows: [
      [
        { type: 'key', key: 'Esc', label: 'Esc' },
        { type: 'key', key: 'Tab' },
        { type: 'modifier', modifier: 'ctrl', label: 'Ctrl' },
        { type: 'modifier', modifier: 'alt', label: 'Alt' },
        { type: 'modifier', modifier: 'shift', label: 'Shift' },
        { type: 'arrow', direction: 'up' },
        { type: 'arrow', direction: 'down' },
        { type: 'arrow', direction: 'left' },
        { type: 'arrow', direction: 'right' },
        { type: 'command', label: 'cmd' },
        { type: 'ime', label: 'IME' },
        { type: 'fontsize', delta: -1, label: 'A−' },
        { type: 'fontsize', delta: 1, label: 'A+' },
      ],
    ],
  },
  claude: {
    rows: [
      [
        { type: 'key', key: 'Esc' },
        { type: 'key', key: '/clear', text: '/clear' },
        { type: 'key', key: '/compact', text: '/compact' },
        { type: 'key', key: '/init', text: '/init' },
        { type: 'key', key: '/memory', text: '/memory' },
        { type: 'key', key: 'y', text: 'y' },
        { type: 'key', key: 'n', text: 'n' },
        { type: 'arrow', direction: 'up' },
        { type: 'arrow', direction: 'down' },
        { type: 'key', key: 'Enter', label: '↵' },
      ],
    ],
  },
}

// Deep-merge a user-provided partial config onto defaults. Keys present on
// the user side fully replace the corresponding default mode (otherwise
// adding one extra slash command would require respelling the whole row).
export function mergeToolbarConfig(
  user: Partial<ToolbarConfig> | undefined | null,
): ToolbarConfig {
  if (!user) return DEFAULT_TOOLBAR_CONFIG
  return {
    normal: user.normal ?? DEFAULT_TOOLBAR_CONFIG.normal,
    claude: user.claude ?? DEFAULT_TOOLBAR_CONFIG.claude,
  }
}
