# agenttui × ptyhost 統合設計 (main-as-base + Adapter graft)

> maultiagent (multi-agent Adapter) と origin/main (no-halt-agent ptyhost restart-survival) の
> `internal/tab/agenttui` を、両方の能力を保って統合する。

## スコープ (2026-07-13 ユーザ確定)
- **含める**: TUI 系 (claude-tui / codex / opencode / generic) の ptyhost **restart-survival** + **multi-agent Adapter**。
- **defer**: claudeagent (stream-json = claude の agent モード) の survival。maultiagent の **in-process 維持**。main の pipe-mode ptyhost 化は follow-up sprint。ただし discover.go の `Mode==pipe` 分岐は**保持** (将来の pipe-mode 共存のため)、`internal/tab/claudeagent/**` は **byte-unchanged** (claude タブ不変の担保)。

## 核心 (なぜ両立できるか)
2 つは**意味的に衝突していない・直交**している。同じ `daemon.go` の同じ接合点を:
- **main** = 「誰が argv を**実行**するか」を変えた: in-process `creackpty` → 独立 `internal/ptyhost` プロセスのクライアント (再起動跨ぎ survival)。
- **maultiagent** = 「誰が argv を**構築**するか」を変えた: inline claude リテラル → `agent.Adapter.SpawnSpec(intent)`。
- 両方が **1 つの opaque `(argv, env, cwd)` タプル**を通る。**ptyhost は claude を一切知らない (ADR-0002: 任意 argv/env/cwd を起動)** → `SpawnSpec.Argv/Env` が `ptyhost.Config.Argv/Env` にそのまま流れる。接合はほぼ 1 行の置換。

## アプローチ B: main をベースに Adapter を graft (生 git merge の手動解決ではない)
理由: daemon.go は 53KB の semantic rewrite collision。conflict marker 解決は survival を失う/ptyhost wiring を落とすリスク。ptyhost/discover/ptyclient は maultiagent に無い純追加。Adapter delta は small localized。

### 採用 (origin/main から verbatim)
- `internal/ptyhost/**` (AgentKind/KillPattern 追加のみ、下記)
- `cmd/palmux/ptyhost.go` + ownership test
- `agenttui/{ptyclient,discover}.go` (`claudetui`→`agenttui` に `git mv` + package 書換のみ)
- `docs/DESIGN/adr/ADR-0001..0003` + `docs/no-halt-agent-design.md`

### 維持 (maultiagent から verbatim — exec 方式に非依存)
- `internal/agent/**` (adapter/claude/codex/opencode/generic/registry/incontainer + tests)
- `internal/tab/agenttab/**`
- FE: agent-registry-store, agent-picker, `tabs/agent-tui` renderer, notify-capability badge, inbox 汎用化

### graft (本作業) — `internal/tab/agenttui/daemon.go`
**origin/main の `claudetui/daemon.go` をベースに**:
1. inline claude arg-builder (`buildClaudeSettings`/`hookEnv`/`--permission-mode`/`--plugin-dir`/`resolveClaudeBin`/`containerClaudeBin`) を **Adapter 呼び出しに置換**。notify URL/hook-bin の host-vs-container 解決 (`notifyURLInContainer`/`containerHookBinPath`) は **main のロジック維持** → `SpawnIntent.Hook` に渡す → `adapter.SpawnSpec(intent)` → `spec.{Argv,Env,PreFiles,KillPattern}`。
2. **seam の下は main のまま**: `spec.Argv` を wrap (host: `argv=spec.Argv`; container: `pc.PTYCommand(daemonCtx, spec.Argv, opts)`) → main の `launchAndAttach(argv,env,cwd)` + survivor-attach + degraded + **replay-drainer** + `restoreScreenJiggle`。maultiagent の `creackpty.Start` path は破棄。
3. `spawnWithArgs` 署名を `(resumeSessionID string, isRespawn bool)` に (Adapter が args 供給)。
4. `respawnLoop`: main の `gateRespawn` (incus-regenerate 待ち、maultiagent に無い=優位) + maultiagent の `immediateFailureBackoff` を統合。resume-ID は `Capabilities().Resume` gate。
5. reap: main の `reapContainerClaude(containerClaudeBin)` を adapter の `spec.KillPattern` に。**live-Daemon** 経路は maultiagent 済み。**GCOrphans** (live Daemon 無し) は `StatusFile.KillPattern` を読む。

### `manager.go`
main ベース (`RunDir()`/`PalmuxBin`/`RunDirOverride`/`GCOrphans` wiring) + maultiagent の **Adapter field + one-manager-per-kind** + `Kind()` accessor + `SessionWatcher`-when-`SessionDiscoverer` gate。

### `internal/ptyhost/server.go` (additive, ADR-0002 準拠)
`Config`+`StatusFile` に **`AgentKind string`** と **`KillPattern string`** を追加 (opaque echo、`RepoID`/`Mode` と同列、ptyhost は解釈しない)。

### `discover.go`
`scanRunDir` の ownership filter を拡張: 既存の `Mode==ModePipe` skip (**保持**) に加え `sf.AgentKind != thisManagerKind` skip。`GCOrphans` は `sf.KillPattern` で in-container reap (hardcode `containerClaudeBin` を置換)。

### `cmd/palmux/main.go`
maultiagent の `agent.BuildRegistry` + per-kind manager loop + `agenttab` 登録を維持。main の boot-time discovery + GC wiring を**各 kind-manager ごとに** (共有 run dir を各自の `AgentKind` で filter)。main の `os.Args[1]=="ptyhost"` dispatch + shutdown 時 `DetachAll` (ptyhost を生かす) を維持。

## seam (一文)
> ptyhost は `Config.Argv/Env/Cwd` を opaque に受ける。daemon は `agent.Adapter.SpawnSpec(intent)` でそのタプルを構築 (host argv、または `pc.PTYCommand`-wrap した in-container argv)。hook/notify/permission/resume は `SpawnIntent` 経由で Adapter に入り、ただの argv/env バイトとして出て ptyhost は検査しない。identity + `AgentKind` + `KillPattern` は `StatusFile` に乗り restart 時の adoption/GC に使う。

## リスク (実装時に必ず確認)
1. **reattach-deadlock 回帰 (P0, Sfeed64-1)**: replay drainer は ATTACH-replay `Feed` の**前**に開始。seam を**上**に graft し、その下 (main の :757-905 相当) を**並べ替えない**。`reattach_deadlock_test.go` で検証。
2. **AgentKind filter 誤設定 → dual-manager eviction loop (Sfeed64-3 のバグ)**: N 個の pty-mode manager が共有 run dir を持つ。`AgentKind` skip は**どの dial よりも前**に (既存 `Mode` skip と同様)。
3. **KillPattern staleness**: config 変更を跨いだ ptyhost は古い pattern を保持 (best-effort reap、document)。
4. **golden-argv drift**: claude の `spec.Argv`→`ptyhost.Config.Argv` が golden と一致すること (host + in-container)。テスト追加。
5. **backoff × gate**: `gateRespawn` (runtime 待ち) と `immediateFailureBackoff` が container-regenerate pause を crash loop に**誤カウントしない**。

## Phasing + 検証 (各段階で build/test green)
- **P1 — mechanical adopt** (振る舞い不変): `internal/ptyhost/**`・`cmd/palmux/ptyhost.go`・`agenttui/{ptyclient,discover}.go` (rename) を採用。daemon は creackpty のまま。`go build` + ptyhost package tests。
- **P2 — graft seam**: `daemon.go spawnWithArgs` を Adapter + `launchAndAttach` に。creackpty 破棄。`hooks.go`/`claude_args.go` 削除 (Adapter が所有)。**検証 (claude 不変)**: golden-argv 等価テスト + main の `daemon_test.go`・`reattach_deadlock_test.go`・`ptyhost_integration_test.go` green。
- **P3 — multiplicity + ownership**: per-kind manager・`AgentKind` field・discovery/GC の per-kind filter・`KillPattern` in StatusFile。**検証 (codex/opencode)**: `codex_test.go`/`opencode_test.go` + 2-kind discovery test (cross-adoption 無し)。
- **P4 — restart-survival E2E**: **codex タブ**で spawn → `systemctl --user restart palmux2` (or kill+relaunch) → subprocess 生存 + tab 再 adopt (画面継続) を assert。次に claude で回帰確認。fresh box (dev-box incus churn 注意)。
- **P5 — orphan-GC E2E**: palmux2 down 中に codex タブ削除 → restart → orphan ptyhost (+ in-container proc、`KillPattern` 経由) が reap されることを assert。

## 主要ファイル
- seam 対象: `internal/tab/agenttui/daemon.go` (base = `git show origin/main:internal/tab/claudetui/daemon.go`)
- Adapter 契約: `internal/agent/adapter.go`
- ptyhost exec 契約 (採用): `origin/main:internal/ptyhost/{doc,server,launch,protocol,paths}.go`
- discovery/GC (採用+拡張): `origin/main:internal/tab/claudetui/{discover,ptyclient}.go`
- wiring: `cmd/palmux/main.go` (main の boot discovery/GC を統合)
