# Lint baseline — S13b16a

**Total: 84 diagnostics across 43 files**
(reported by eslint summary: 77 errors + 9 warnings = 86 problems)

## Per-rule totals

| Rule | Count |
|------|------:|
| `react-hooks/set-state-in-effect` | 38 |
| `react-refresh/only-export-components` | 21 |
| `react-hooks/refs` | 9 |
| `react-hooks/exhaustive-deps` | 6 |
| `react-hooks/rules-of-hooks` | 2 |
| `react-hooks/immutability` | 2 |
| `no-empty` | 2 |
| `'react-hooks/exhaustive-deps')` | 1 |
| `@typescript-eslint/no-unused-vars` | 1 |
| `@next/next/no-img-element` | 1 |
| `@typescript-eslint/no-unused-expressions` | 1 |

## Per-file breakdown (sorted by file path)

### S13b16a-1 (set-state-in-effect)

| File | Rule | Count |
|------|------|------:|
| `frontend/src/components/bottom-sheet/bottom-sheet.tsx` | `react-hooks/set-state-in-effect` | 1 |
| `frontend/src/components/branch-picker.tsx` | `react-hooks/set-state-in-effect` | 1 |
| `frontend/src/components/command-palette/command-palette.tsx` | `react-hooks/set-state-in-effect` | 3 |
| `frontend/src/components/drawer.tsx` | `react-hooks/set-state-in-effect` | 2 |
| `frontend/src/components/pill-select/pill-select.tsx` | `react-hooks/set-state-in-effect` | 1 |
| `frontend/src/components/repo-delete-modal.tsx` | `react-hooks/set-state-in-effect` | 1 |
| `frontend/src/components/repo-picker.tsx` | `react-hooks/set-state-in-effect` | 2 |
| `frontend/src/components/subagent-cleanup-dialog.tsx` | `react-hooks/set-state-in-effect` | 1 |
| `frontend/src/components/toolbar/toolbar.tsx` | `react-hooks/set-state-in-effect` | 1 |
| `frontend/src/components/user-commands-modal.tsx` | `react-hooks/set-state-in-effect` | 1 |
| `frontend/src/components/workspace-actions.tsx` | `react-hooks/set-state-in-effect` | 1 |
| `frontend/src/hooks/use-section-collapsed.ts` | `react-hooks/set-state-in-effect` | 1 |
| `frontend/src/tabs/claude-agent/blocks/plan.tsx` | `react-hooks/set-state-in-effect` | 1 |
| `frontend/src/tabs/claude-agent/claude-run-button.tsx` | `react-hooks/set-state-in-effect` | 1 |
| `frontend/src/tabs/claude-agent/composer/index.tsx` | `react-hooks/set-state-in-effect` | 1 |
| `frontend/src/tabs/claude-agent/composer/selectors.tsx` | `react-hooks/set-state-in-effect` | 3 |
| `frontend/src/tabs/claude-agent/conversation-export.tsx` | `react-hooks/set-state-in-effect` | 1 |
| `frontend/src/tabs/claude-agent/conversation-search.tsx` | `react-hooks/set-state-in-effect` | 1 |
| `frontend/src/tabs/claude-agent/history-popup.tsx` | `react-hooks/set-state-in-effect` | 1 |
| `frontend/src/tabs/files/file-preview.tsx` | `react-hooks/set-state-in-effect` | 4 |
| `frontend/src/tabs/files/files-move-modal.tsx` | `react-hooks/set-state-in-effect` | 1 |
| `frontend/src/tabs/files/files-upload-modal.tsx` | `react-hooks/set-state-in-effect` | 1 |
| `frontend/src/tabs/files/files-view.tsx` | `react-hooks/set-state-in-effect` | 2 |
| `frontend/src/tabs/git/git-monaco-diff.tsx` | `react-hooks/set-state-in-effect` | 1 |
| `frontend/src/tabs/git/git-view.tsx` | `react-hooks/set-state-in-effect` | 4 |

**Subtotal: 38**

### S13b16a-2 (refs-during-render)

| File | Rule | Count |
|------|------|------:|
| `frontend/src/components/divider.tsx` | `react-hooks/refs` | 1 |
| `frontend/src/tabs/claude-agent/scroll-hooks.ts` | `react-hooks/refs` | 1 |
| `frontend/src/tabs/claude-agent/top-bar.tsx` | `react-hooks/refs` | 1 |
| `frontend/src/tabs/files/file-preview.tsx` | `react-hooks/refs` | 1 |
| `frontend/src/tabs/files/viewers/drawio-view.tsx` | `react-hooks/refs` | 2 |
| `frontend/src/tabs/files/viewers/monaco-view.tsx` | `react-hooks/refs` | 3 |

**Subtotal: 9**

### S13b16a-3 (zoo)

| File | Rule | Count |
|------|------|------:|
| `frontend/src/components/branch-picker.tsx` | `react-hooks/exhaustive-deps` | 1 |
| `frontend/src/components/context-menu/confirm-dialog.tsx` | `react-refresh/only-export-components` | 1 |
| `frontend/src/components/context-menu/prompt-dialog.tsx` | `react-refresh/only-export-components` | 1 |
| `frontend/src/components/context-menu/select-dialog.tsx` | `react-refresh/only-export-components` | 1 |
| `frontend/src/components/diff/diff-view.tsx` | `react-refresh/only-export-components` | 1 |
| `frontend/src/components/drawer.tsx` | `react-refresh/only-export-components` | 1 |
| `frontend/src/components/inline-completion/use-inline-completion.ts` | `'react-hooks/exhaustive-deps')` | 1 |
| `frontend/src/components/main-area.tsx` | `react-hooks/exhaustive-deps` | 2 |
| `frontend/src/components/repo-delete-modal.tsx` | `@typescript-eslint/no-unused-vars` | 1 |
| `frontend/src/components/tab-bar.tsx` | `react-hooks/rules-of-hooks` | 2 |
| `frontend/src/hooks/use-focused-terminal.ts` | `react-hooks/exhaustive-deps` | 1 |
| `frontend/src/tabs/claude-agent/blocks/index.tsx` | `react-refresh/only-export-components` | 2 |
| `frontend/src/tabs/claude-agent/composer/selectors.tsx` | `react-refresh/only-export-components` | 2 |
| `frontend/src/tabs/claude-agent/conversation-export.tsx` | `react-refresh/only-export-components` | 3 |
| `frontend/src/tabs/claude-agent/conversation-search.tsx` | `react-refresh/only-export-components` | 4 |
| `frontend/src/tabs/claude-agent/search-context.tsx` | `react-refresh/only-export-components` | 1 |
| `frontend/src/tabs/claude-agent/top-bar.tsx` | `react-refresh/only-export-components` | 2 |
| `frontend/src/tabs/claude-agent/user-turn-editor.tsx` | `react-hooks/exhaustive-deps` | 1 |
| `frontend/src/tabs/claude-agent/user-turn-editor.tsx` | `react-refresh/only-export-components` | 1 |
| `frontend/src/tabs/files/file-list.tsx` | `react-hooks/immutability` | 2 |
| `frontend/src/tabs/files/files-view.tsx` | `react-hooks/exhaustive-deps` | 1 |
| `frontend/src/tabs/git/git-image-diff.tsx` | `@next/next/no-img-element` | 1 |
| `frontend/src/tabs/git/git-image-diff.tsx` | `react-refresh/only-export-components` | 1 |
| `frontend/src/tabs/git/git-view.tsx` | `@typescript-eslint/no-unused-expressions` | 1 |
| `frontend/src/tabs/git/git-view.tsx` | `no-empty` | 2 |

**Subtotal: 37**


## Story scope assignment

- **Story S13b16a-1**: `react-hooks/set-state-in-effect` rule fixes (the bulk).
- **Story S13b16a-2**: `react-hooks/refs` (Cannot update/access ref during render) fixes.
- **Story S13b16a-3**: everything else (`react-refresh/only-export-components`, `no-empty`, `no-unused-expressions`, config additions, etc.) + final `lint=0` gate.
