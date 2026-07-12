# no-halt-agent 設計書 — palmux2 再起動を跨ぐ Claude agent 生存機構

> ADR: [ADR-0001](DESIGN/adr/ADR-0001-detached-ptyhost-for-agent-survival.json) (detached ptyhost) /
> [ADR-0002](DESIGN/adr/ADR-0002-thin-holder-ptyhost.json) (thin holder) /
> [ADR-0003](DESIGN/adr/ADR-0003-cgroup-escape-systemd-run-scope.json) (cgroup 脱出) /
> [ADR-0004](DESIGN/adr/ADR-0004-agent-pipe-mode-offset-replay.json) (agent pipe モード + offset replay)

## 0. 問題と目標

claude (tui / agent 両モード) は palmux2 の直接の子プロセスであり、palmux2 の再起動
(self-update / `systemctl --user restart palmux2` / `make serve` 入れ直し) で必ず死ぬ。
`--resume` で会話は復元されるが**実行中のターンは殺される**。

目標: **tmux が Bash タブに提供しているのと同じ生存性を claude タブに与える。**
palmux2 を何度再起動しても、走っている claude は走り続け、再起動後の palmux2 が再接続する。

非目標:
- ホスト再起動を跨ぐ生存 (tmux も生き残らない。従来通り `--resume` 復元)
- palmux2 停止中に claude が死んだ場合の自動 respawn (次回起動時に `--resume` で新規 spawn = 現状と同等)

## 1. プロセスモデル

```
palmux2 (systemd user unit / make serve)
  │  unix socket (再起動を跨いで再接続)
  ▼
palmux ptyhost  ×  タブごと 1 プロセス (別 cgroup: systemd-run --user --scope)
  │  PTY (tui モード) / stdio pipe (agent モード)
  ▼
claude            … host runtime: 直接 exec
                  … incus runtime: ptyhost が `incus exec -t …` wrapper を保持
                     (wrapper が ptyhost 側で生き残るため in-container claude も生存)
```

- **ptyhost は palmux 単一バイナリのサブコマンド** (`palmux ptyhost`)。配布は不変。
- **thin holder (ADR-0002)**: ptyhost が持つのは
  「1 claude プロセスインスタンス + PTY/pipe + 絶対 offset 付き raw ring + unix socket」のみ。
  respawn・`--resume`・hook 設定生成 (`hooks.go`)・引数組み立て (`claude_args.go`)・
  incus regenerate ゲート (`gateRespawn`)・Emulator・roleCoordinator・SessionWatcher は
  **すべて palmux2 側に残す**。
- **respawn = 新 ptyhost の spawn**。claude が exit したら ptyhost は exit status を
  socket 応答 (STATUS) と status ファイルに記録して自分も終了する。palmux2 側の
  respawnLoop 相当が STATUS/切断を検知して新しい ptyhost を spawn する
  (`--resume <sid>` の判断も palmux2)。
- ptyhost の spawn 引数には palmux2 が組み立て済みの完全な argv・env・cwd を渡す。
  ptyhost は claude 固有の知識を持たない (= 汎用プロセスホルダー)。

### spawn 経路 (ADR-0003)

1. `systemd-run --user --scope --collect --unit palmux-agent-<instancePrefix>-<hash> -- palmux ptyhost …` を試行
2. 失敗 (非 systemd / D-Bus user session 不在) → setsid + double-fork で detach

判定は実行時 (試して fallback)。NixOS module / hm module は無改造。

## 2. socket protocol

フレーム化された最小プロトコル。**凍結を前提に設計する** — ptyhost は数週間古いバイナリの
まま生きるため、メッセージ追加は ADR-0002 違反シグナルとして扱う。

| メッセージ | 方向 | 内容 |
|---|---|---|
| `HELLO` | 双方向 | protocol version + mode (pty/pipe) + pid + 開始時 argv hash。version 不一致は切断せず palmux2 が UI degrade |
| `ATTACH {offset}` | c→h | offset 以降の ring replay + 以後 live bytes を購読。`offset=-1` は「ring 先頭から」 |
| `DATA {offset, bytes}` | h→c | ring 内容 (replay / live 共通。絶対 offset 付き) |
| `INPUT {bytes}` | c→h | claude stdin (PTY master / pipe) への書き込み |
| `RESIZE {cols, rows}` | c→h | PTY winsize (pipe モードでは no-op) |
| `ACK {offset}` | c→h | 処理済み offset の通知 (agent モードのロスレス replay 用。tui では任意) |
| `STATUS` | c→h 要求 / h→c 応答 | pid・alive・exit status・ring 使用量・ring 先頭 offset |
| `SHUTDOWN {signal}` | c→h | claude へ SIGTERM→(timeout)→SIGKILL して ptyhost 終了 (orphan GC / タブ削除用) |

- version 不一致時: 殺さない。UI に「このタブは旧世代の agent ホストで動いています —
  再起動すると新機能が有効になります」と表示し、INPUT/DATA の最低互換だけ維持できない場合は
  「再起動して再接続」を促す。
- 複数クライアント: palmux2 だけが socket に繋ぐ (ブラウザの multi-client は従来通り
  palmux2 内の Ring/roleCoordinator が捌く)。socket は同時 1 接続で十分。

## 3. 配置・発見・再接続

```
$XDG_RUNTIME_DIR/palmux/<instancePrefix>/          # fallback: <config-dir>/run/
  <repoId>__<branchId>__<tabId>.sock
  <repoId>__<branchId>__<tabId>.json               # pid, mode, argv hash, 開始時刻, exit status
```

- `<instancePrefix>` はデフォルト `palmux`、`--tmux-prefix` と同様に INSTANCE=dev rig では
  `pmx_dev` 等に分離 → 並走 instance が互いの ptyhost を claim / GC しない。
- **palmux2 起動時**: dir を走査 → 各 socket に HELLO → 生きていれば Daemon (thin client 化した
  claudetui.Daemon / claudeagent.Client) を「既存 ptyhost に接続」モードで再構築。
  接続不能 / pid 死亡 → socket + status ファイルを掃除し、必要なら `--resume` で新規 spawn。
- **orphan GC**: 既存の sync ループ (10s scan) に相乗り。socket に対応する
  (repoId, branchId, tabId) が store に存在しない (タブ削除 / worktree 消失 / branch close) →
  `SHUTDOWN` 送信。tmux zombie kill と同型。

## 4. 既存コードからの移行マップ (claudetui)

| 現 daemon.go の要素 | 行き先 |
|---|---|
| PTY 生成 (`creackpty.Start`) + readLoop + WriteInput + Resize | **ptyhost** |
| `Ring` (絶対 offset 付きに拡張) | **ptyhost** (palmux2 側にも表示用の短い Ring を残してよい) |
| `Emulator` + feedMu + RenderSnapshotAndSubscribe | palmux2 (socket からの DATA を feed) |
| `roleCoordinator` (multi-client) | palmux2 (無改造) |
| respawnLoop + sessionIDReady + `--resume` | palmux2 (「exit 検知 → 新 ptyhost spawn」に変形) |
| `gateRespawn` (incus regenerate 待ち) | palmux2 (respawn = spawn 前のゲートなので自然に残る) |
| hooks.go の settings/env 注入 | palmux2 (spawn 時に argv/env として ptyhost へ渡す) |
| `PTYCommander` (incus exec -t) | **ptyhost が wrapper プロセスを保持** (palmux2 が組み立てた argv を渡す) |
| Shutdown (SIGTERM→SIGKILL) | ptyhost (`SHUTDOWN` メッセージで駆動) |

claudeagent 側 (Sprint 2) は `client.go` の cmd 構築 + stdin/stdout/stderr pump を
ptyhost (pipe モード) 越しに差し替える。stream-json の parse・MCP 権限サーバ・permstate・
transcript は palmux2 に残る (ADR-0004)。

## 5. 再接続時の画面復元 (tui) — 受入基準つき

手順:
1. socket ATTACH (ring 先頭 offset から replay)
2. 新規 Emulator に replay を feed (途中からのバイト列なので一時的に乱れてよい)
3. **SIGWINCH ジグル**: RESIZE(cols, rows-1) → RESIZE(cols, rows) を送り claude に全再描画させる
4. 以後 live DATA を通常 feed

受入基準 (E2E):
- palmux2 再起動後の再接続で、再起動前に表示されていた claude TUI の内容
  (会話・ステータスライン) が視認可能に復元される
- 再起動中に claude が出力を続けていた場合 (実行中ターン)、その出力が失われず表示される
- ジグル後の画面に残骸 (前フレームの断片) が残らない

収束しない TUI 状態が見つかったら ADR-0002 の revisit 条件に該当 (→ emulator を ptyhost 側へ
移す fat 化を再検討)。

## 6. agent モードのロスレス replay (Sprint 2, ADR-0004)

- ptyhost (pipe モード) は stdout を**行単位 + 絶対 offset** で ring に貯める
  (stream-json は 1 イベント = 1 行)。stderr は別チャネルで同様に。
- palmux2 は行を処理するたび `ACK {offset}` を送り、**ack 済み offset を永続化**
  (sessions.json 相当)。再接続時は last-ack 以降を replay → transcript / permstate /
  pending な control_request を漏れなく再構成。
- ring 溢れ (長時間切断) を検出したら**黙って欠けた transcript を出さず**、
  「ロスレス復元不能」を明示して新規セッション扱いにする。
- **spike (Sprint 2 冒頭で最初に実施)**: palmux2 の再起動時間 (数秒〜十数秒) を跨いだ
  MCP control_request への応答を claude CLI が受理するか。応答期限が短ければ
  ADR-0004 の notes に従い replay 設計を見直す。

## 7. Sprint 分割

### Sprint 1 — ptyhost 基盤 + claude-tui 生存
- `palmux ptyhost` サブコマンド (pty モード) + socket protocol + spawn 経路 (systemd-run/setsid)
- claudetui.Daemon の thin client 化 (§4 の移行)
- 発見・再接続・orphan GC・INSTANCE 分離
- E2E: §5 の受入基準 + **実 systemd 環境で `systemctl --user restart palmux2` を跨いだ
  生存 (SURVIVAL_PASS、Sa8e7d0 と同型)** + incus runtime での生存 (in-container claude が
  再起動を跨いで同一 pid のまま)
- spike (Sprint 冒頭): systemd-run --user scope が Ubuntu/hm・NixOS アプライアンス両方で
  期待通り cgroup 分離すること

### Sprint 2 — pipe モード + claude-agent ロスレス生存
- ptyhost pipe モード + 行 ring + ACK/offset 永続化
- claudeagent.Client の差し替え、permstate/transcript の replay 再構成
- spike (冒頭): §6 の control_request 応答期限
- E2E: 再起動を跨いだ permission 要求→応答の成立、transcript 無欠損

## 8. リスクと未解決事項

| リスク | 対処 |
|---|---|
| SIGWINCH ジグルで画面が収束しない TUI 状態 | 受入テストで検出 → ADR-0002 revisit (fat 化) |
| MCP control_request の応答期限 | Sprint 2 冒頭 spike (ADR-0004 notes) |
| 旧 ptyhost と新 palmux2 の protocol skew | HELLO version handshake + UI degrade (§2) |
| in-container claude の reap (S4d8b1c backlog と同根) | SHUTDOWN 時の KillContainerProcesses 相当を palmux2 側 GC が実施 |
| $XDG_RUNTIME_DIR 不在環境 | `<config-dir>/run/` fallback (パーミッション 0700) |
