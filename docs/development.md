# Palmux v2 開発ワークフロー

このドキュメントは、palmux2 自身の中で palmux2 を開発するときに「ホスト用 instance を巻き込まずに開発を進める」ための手順をまとめる。

## なぜ専用 worktree が必要か

palmux2 は tmux セッションを管理する。普段使いの palmux2（"ホスト用"）の中で `make serve` を打つと、ホスト用バイナリが再起動される。再起動時に **そこで動いている tmux セッション = 自分が今操作している Claude CLI が一緒に死ぬ**。これは明白な bootstrap 問題で、対話履歴が `claude --resume` で復活できる場面はあるが、permission 待ちなど in-flight なツール呼び出しは壊れる。

→ **開発用 instance は別の portman 名・別の config dir で動かし、ホスト用 instance には触らない**。

## 1 回だけやる準備

### 1. 開発用ブランチ + worktree を切る

```bash
# main にいる状態で（この手順は1回だけ）
gwq add -b dev
gwq cd dev   # or `cd "$(gwq get dev)"`
```

`gwq` は `~/worktrees/{host}/{owner}/{repo}/{branch}` に worktree を置く（gwq の `naming.template` 設定に従う）。palmux2 の場合は `~/worktrees/github.com/tjst-t/palmux2/dev`。

このディレクトリは **独立した working tree** で、git の履歴はホスト用 worktree と同じリポジトリを共有する。`./tmp/` (config dir) は worktree 内なので自動的に分離される。

### 2. Open する側の palmux2 から `dev` ブランチを認識させる

開発用 instance を起動するとき、palmux2 の "ブランチ Open" モデル（`docs/original-specs/01-architecture.md`）により、worktree が存在するだけで Open 扱いになる。ホスト用 palmux2 でも `dev` ブランチを開けるようになっているので、**ホスト用の palmux2 から開発用 instance を起動・操作することができる**（ただし開発用 instance への attach はブラウザ別タブから行う）。

## 通常の開発サイクル

### 開発用 instance の起動

開発用 worktree に入り、`INSTANCE=dev` を付けて `make` を呼ぶ:

```bash
gwq cd dev   # ~/worktrees/github.com/tjst-t/palmux2/dev に移動

# Hot-reload あり (vite dev + go run)
make dev INSTANCE=dev

# 単体バイナリで稼働確認 (frontend embed 込み)
make serve INSTANCE=dev
```

`INSTANCE=dev` は `Makefile` の portman 名 / PID ファイル / ログファイルにサフィックスを付ける:

| ターゲット                | 通常の portman 名         | `INSTANCE=dev` 時             |
| ------------------------- | ------------------------- | ----------------------------- |
| `make serve`              | `palmux2`                 | `palmux2-dev`                 |
| `make dev` (api)          | `palmux2-api`             | `palmux2-dev-api`             |
| `make dev` (frontend)     | `palmux2-frontend`        | `palmux2-dev-frontend`        |

| ファイル                | 通常                          | `INSTANCE=dev` 時                  |
| ----------------------- | ----------------------------- | ---------------------------------- |
| serve PID               | `tmp/palmux.pid`              | `tmp/palmux-dev.pid`               |
| serve ログ              | `tmp/palmux.log`              | `tmp/palmux-dev.log`               |
| serve portman env       | `tmp/palmux.portman.env`      | `tmp/palmux-dev.portman.env`       |

portman が同名に対しては毎回同じポートを返すので、開発用ポートも安定する。

`make serve` はバックグラウンド起動。再度 `make serve` を打つと PID ファイルから古いプロセスを kill してから新しく立ち上げる。停止だけしたいときは `make serve-stop`、ログを見たいときは `make serve-logs`。

### ブラウザで開く

`make dev INSTANCE=dev` の出力（または `tmp/portman.env`）に、割り当てられたポートが `PALMUX2_DEV_FRONTEND_PORT` / `PALMUX2_DEV_API_PORT` として表示される。例:

```
PALMUX2_DEV_FRONTEND_PORT=53210
PALMUX2_DEV_API_PORT=53211
```

vite dev の場合: `http://<host>:53210/` を開く。
production 単体バイナリ (`make serve INSTANCE=dev`) の場合: portman が割り当てたポート 1 つだけ。

ホスト用 palmux2 (8207 など) と完全に独立したポートなので、**両方のブラウザタブを並べて作業できる**。

### コミットとマージ

開発用 worktree は `dev` ブランチに紐づいている。普通に編集→ `git commit` → `git push` でよい。`main` にマージしたい場合:

```bash
# ホスト用 worktree (main) で
git pull --ff-only origin main
git merge dev          # or rebase
git push origin main
```

## トラブルシューティング

### 「ポートが衝突する」

`portman env --name palmux2-dev-api --name palmux2-dev-frontend --output -` で現在のリースを確認。`portman release --name palmux2-dev-api` で剥がせる。

### 「`gwq cd dev` で worktree が見つからない」

`gwq list` で確認。なければ `gwq add -b dev` をやり直す。

### 「ホスト用 palmux2 を再起動したい」

ホスト用 worktree (`/home/ubuntu/ghq/github.com/tjst-t/palmux2`) で `make serve` を打つ。**再起動するとそこで動いている Claude CLI が落ちる**ので、進行中の作業を保存してから。`claude --resume <session_id>` で会話は復活できる（が、ツール途中の状態は復元できないことがある）。

### 「開発用 worktree の Claude CLI を再起動したい」

開発用 instance の中の Claude タブから `/clear` または history popup で別セッションを resume。あるいは開発用 worktree でツール呼び出しを終了させてから `make` を再実行。**ホスト用 instance には影響しない**。

## 開発用 worktree を片付ける

```bash
# 別ブランチに移動してから
cd ~/ghq/github.com/tjst-t/palmux2
gwq remove dev          # gwq 経由で削除
git branch -D dev       # 必要なら
```

`gwq remove` の前に `dev` worktree 内に未コミットの変更がないことを確認する。
