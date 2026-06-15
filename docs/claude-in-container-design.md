# Claude をコンテナ内で動かす設計 (Option B) — S4d8b1c

> incus-container Workspace で `claude` CLI 本体を **ホストではなくコンテナ内**で動かす。
> これにより (1) Claude のツール実行 (Bash/npm/pip/ファイル) が真にコンテナ隔離され、
> (2) `palmux-browser` skill + CLI がコンテナ内 Claude から使えるようになる。

## 1. 動機 (なぜやるか)

### 1.1 隔離の穴 (本質的な問題)

incus-container runtime は「npm-g/pip/apt・プロセス・ポートの host 汚染を防ぐ」ために
Workspace をコンテナで隔離する設計 (S8478ca)。しかし現状:

- **ユーザが開く Bash/ターミナルタブ** は runtime の tmux client 経由で
  `incus exec -t <inst> -- tmux attach …` としてコンテナ内で動く ✓
- **Claude タブ (agent / tui どちらも)** は palmux ホストプロセスから
  `exec.CommandContext(ctx, claudeBin, args…)` で **ホスト直叩き** され、コンテナを通らない ✗

結果として、**Claude が Bash ツールで `npm install` / `pip install` / ファイル生成を行うと
それは host 上で実行され、host を汚染する**。incus 隔離の主目的が Claude に関して半分破れている。

参照: `internal/tab/claudeagent/client.go:153`、`internal/tab/claudetui/daemon.go:358`
(どちらも runtime を経由しないホスト exec)。

### 1.2 browser skill / CLI が届かない (S62374c の積み残し)

`palmux-browser` の skill + CLI は **コンテナ image にのみ**焼かれている
(`/usr/local/bin/palmux-browser`、`/usr/local/share/palmux/.claude/skills/palmux-browser/`)。
Claude はホストで動くので、これらが見えない。S62374c は `--add-dir` で skill が読まれる前提だったが、
**`--add-dir` はファイルアクセス許可を与えるだけで skill を登録しない** (正しくは `--plugin-dir`)。
仮に `--plugin-dir` に直しても、ホストにそのパスが無いので解決しない。

→ Claude をコンテナ内で動かせば、skill + CLI が同一コンテナ内に揃い、両問題が同時に解決する。

## 2. 現状アーキテクチャ (調査結果)

### 2.1 Claude tab は 2 つ、どちらもホスト exec

| タブ実装 | transport | spawn | 経路 |
|---|---|---|---|
| `claudeagent` ("Claude" タブ既定、agent モード) | stream-json + MCP、**素のパイプ** (PTY 無し)、stderr 分離 | `exec.CommandContext` (`client.go:153`)、`cmd.Dir`=worktree、`cmd.Env` 未設定 (host env 継承) | host |
| `claudetui` (TUI モード) | **PTY** (creack/pty) → ring buffer + headless emulator → WS | `creackpty.Start(exec.Command(claudeBin, args…))` (`daemon.go:358`)、`cmd.Dir`=worktree | host |

mode は per-tab 設定 (`claude_mode: agent|tui`、Sadf90e)。`agent` が既定。

### 2.2 Bash タブは既にコンテナ内 (再利用すべき実証済み経路)

`handler_ws.go` の attach → `store.TmuxFor` (incus なら `incusTmuxClient`) →
`tc.Attach(...)` = `incus.go:1532` `AttachByIndex`:

```
attachArgs := append([]string{"exec","-t",c.inst}, userExecFlags()...)
attachArgs = append(attachArgs, "--","tmux","attach-session","-t",target)
cmd := exec.CommandContext(ctx,"incus",attachArgs...)
f,_ := pty.StartWithSize(cmd, &pty.Winsize{...})   // host 側 creack/pty master
```

→ **コンテナ内インタラクティブ PTY = host 側 `creack/pty` で `incus exec -t … -- <cmd>` を包む**。
これが Claude をコンテナ内で TUI 表示する際にそのまま使える形。

### 2.3 runtime が提供する primitive

| method | 性質 | claude 用途 |
|---|---|---|
| `Exec` (`incus.go:903`) | **capture 専用** (stdin=nil、stdout/stderr をバッファ) | ✗ インタラクティブ不可 |
| `AttachTmuxSession` (`incus.go:1299`) | `incus exec -t … -- tmux attach` を host pty で包む `io.ReadWriteCloser` | TUI に流用可 |
| `TmuxClient().Attach/AttachByIndex` (`incus.go:1523`) | 同上 (bash タブが使う load-bearing 経路) | TUI に流用可 |
| `userExecFlags()` (`incus.go:892`) | `--user 1000 --group 1000 --env HOME=/home/ubuntu --env USER=ubuntu` | 共通注入 |

**未提供**: 非-PTY の双方向 stdin/stdout + 分離 stderr の interactive exec (agent モードに必要)。

### 2.4 既にマウント済み (= コンテナ内で利用可能、同一パス・uid 整合)

`incus.go` Start の mounts: `~/ghq`(worktree)・`~/.claude`・`~/.claude.json`・
`~/.local/share/claude`・`~/.local/bin`(claude バイナリ)・dotfiles・`~/.gitconfig`・
`~/.config/gh`・`~/.ssh`。`raw.idmap "both 1000 1000"` で uid 整合。

→ **コンテナ内 claude は再認証不要** (`~/.claude` 共有)、worktree も同一パスで見える。

### 2.5 実機で確認済みの事実

先行検証で `incus exec <inst> --user 1000 … -- /home/ubuntu/.local/bin/claude --plugin-dir <plugin> --print "…"`
を実行 → claude がコンテナ内で起動し、**認証が効き** (実応答が返った)、
**`--plugin-dir` で skill が `palmux:palmux-browser` としてロードされた**。
核心 (claude-in-container + 認証 + skill) は de-risk 済み。

## 3. 設計

### 3.1 共通: claudetui/claudeagent → runtime ハンドルの seam

両タブ実装は現在 worktree **パス文字列**しか受け取らない (`WorktreeResolver`)。
attach 時に `Store.CurrentRuntime(repoID, branchID)` を解決して runtime を渡す seam を追加
(bash タブの `store.TmuxFor` と同じ発想)。runtime.Kind() が host なら従来どおりホスト exec、
incus なら下記のコンテナ経路に切替える。

### 3.2 Phase 1: claude-tui (TUI モード) を in-container 化

daemon が PTY で包むプロセスを host claude → コンテナ claude に差し替えるだけ。
ring/emulator/readLoop/resize/respawn は **無改造**:

```
host:  exec.Command(claudeBin, args...)
incus: exec.Command("incus","exec","-t",inst, userExecFlags()...,
         "--cwd",worktree, "--env","PALMUX_NOTIFY_URL=<bridge>", "--env",…,
         "--", "/home/ubuntu/.local/bin/claude", args...)
```

- PTY master は host 側 (creack/pty が incus-exec クライアントを包む)、claude はコンテナ内。
- daemon の `*os.File`/`creackpty.Setsize` 前提を `io.ReadWriteCloser`+`ResizeFunc` 形 (runtime の `ptyConn`) に抽象化する小改修が要る。
- `--plugin-dir <skill plugin>` をここで注入 (= `--add-dir` を置換)。skill を plugin レイアウト化 (`.claude-plugin/plugin.json` + `skills/`) する必要あり (image / runtime いずれかで用意)。

### 3.3 Phase 2: claude-agent (agent モード) を in-container 化

- transport が **PTY 無しの素パイプ + 分離 stderr** なので、runtime に
  **新 primitive `ExecInteractive`** (= `incus exec` を `-t` 無しで双方向 stdin/stdout + 別 stderr パイプに繋ぐ) を追加。
- **MCP は palmux プロセス内**で stdio 上を流れるだけなので、パイプを忠実に proxy すれば**無改造で動く** (`--mcp-config` も使っていない)。
- respawn (`--add-dir`/`--effort`/`--permission-mode`/stale-resume) は同 primitive を再呼び出し。

### 3.4 付随変更 (共通)

| 項目 | 対応 |
|---|---|
| **PATH** | claude を**絶対パス** `/home/ubuntu/.local/bin/claude` で起動 (非ログイン `incus exec` は `~/.local/bin` を PATH に持たない)。検証済みで動く |
| **hook binary** | 稼働中 palmux バイナリ (CGO 無し静的 linux amd64) をコンテナに **bind-mount** し `hookBinPath` をそのコンテナパスに。version 自動同期 |
| **NOTIFY_URL** | コンテナ用に **bridge gateway URL** を注入 (現状 `127.0.0.1` はコンテナ自身を指す)。bridge listener は既存 (`incusBridgeListenAddr`) |
| **env** | `HOME/USER` は userExecFlags 済、`PALMUX_*` hook env を `--env` で注入、OAuth/認証は `~/.claude` マウントで充足 |
| **プロセス kill** | host 側 incus-exec ラッパ kill ではコンテナ内 claude が残る恐れ → signal 伝播 or 明示 PID kill |
| **画像添付 upload dir** | コンテナ未マウント → マウント追加 (image paste) |

## 4. リスク / 要検証

1. **(最大の未知)** agent モードの stream-json を `incus exec` (**必ず -t 無し**) パイプで流したとき、
   stdout/stderr が binary-clean に分離されるか (`-t` は混ざるので不可)。
2. SessionStore の fsnotify (host が `~/.claude/projects` を watch、コンテナが同 inode に書く) が in-container 書込を拾うか。
3. long-lived `incus exec` セッションの安定性・レイテンシ (1 本張りっぱなしなので影響小の想定)。
4. respawn が incus exec 経路でも成立するか。
5. hook 用 palmux バイナリのコンテナ mount が静的バイナリとして問題なく動くか (CGO 無しなので想定 OK)。

## 5. 段階導入

- **Phase 1 (S4d8b1c-1)**: claude-tui を in-container 化 + `--plugin-dir` 化。
  ユーザ実利用モード・browser skill ターゲット・隔離穴塞ぎを一手に。変更は局所的、核心は de-risk 済み。
- **Phase 2 (S4d8b1c-2)**: claude-agent を in-container 化 (stream-json 用 `ExecInteractive` primitive)。
  Phase 1 で hook/notify/PATH/mount/seam の足場が出来てから。

## 6. 検証計画 (実機・本番モード)

deploy VM (`palmux-deploy-test.tjstkm.net`、incus-container) で:

- TUI モードで Claude タブを開く → **コンテナ内に claude プロセスが立つ** (`pgrep` をコンテナ内で確認)。
- `/skills` に `palmux:palmux-browser` が出る。
- Claude に「ブラウザで example.com 開いてスクショ」と頼む → `palmux-browser` 経由で noVNC に反映。
- **隔離検証**: Claude の Bash ツールで `hostname` / `npm install` を実行 → **コンテナ内**で動く
  (hostname=コンテナ名、host の node_modules を汚さない) ことを確認。
- agent モード (Phase 2) は stream-json 往復 + MCP 権限フロー + respawn を本番モードで確認。

## 7. 参照 (調査根拠)

- claudeagent spawn/transport/MCP: `internal/tab/claudeagent/client.go:153,100-148,156-167,184,346,428`、`manager.go:642,1672`
- claudetui daemon PTY: `internal/tab/claudetui/daemon.go:358,369,462-517,642-675`、`provider.go:117-193`
- runtime primitive: `internal/runtime/runtime.go:132-184`、`internal/runtime/incus/incus.go:892,903,1299,1523-1561`
- bash 経路: `internal/server/handler_ws.go:252-291`
- mounts: `internal/runtime/incus/incus.go:344-404`
- hook/notify: `internal/tab/claudetui/hooks.go:27,35,65`、`cmd/palmux/main.go:336-348,581-614`
- image skill/CLI: `images/workspace-default/build.sh:211-253`
