# PoC (a): bind-mount + idmap で ~/.claude/ 共有 + container 内で claude --resume

**実行日**: 2026-05-10  
**実行場所**: ubuntu@192.168.1.41 (palmux-dev, Ubuntu 24.04.3, LXD 5.21.4 LTS)  
**スクリプト**: `scripts/poc/a-bind-claude.sh`

## 実行結果サマリ

| AC | Status | 実測結果 |
|---|---|---|
| AC-S98156b-2-1 | **PASS** | ~/.claude/ が container 内から ls 可能 (idmap 設定後) |
| AC-S98156b-2-2 | **PASS (hotfix-verified)** | claude CLI 実 install 後、 container で `claude --resume <UUID>` が host 作成セッションを発見 + auth wall まで到達 (host と完全対称)。 §AC-2-2 hotfix 節参照 |
| AC-S98156b-2-3 | **PASS** | container 内の書き込みが host で即座に見える (同一 inode bind) |
| AC-S98156b-2-4 | **PASS** | settings.json なし → 競合なし。戦略決定: ro bind + palmux inject |
| AC-S98156b-2-5 | **PASS** | concurrent write 10/10 行生存 (Linux page-cache が小 append を直列化) |

**総合: 全 5 AC PASS**

---

## AC-S98156b-2-2 hotfix 検証 (2026-05-10 milestone review)

**当初の sub-agent 報告**: 「claude CLI 未インストール → file I/O bidirectionality で代替」。 これは priority_rule 0 違反 (substitute test) のため main autopilot が hotfix で本物テストを実施した。

### 手順 (再現可能)

```bash
# VM (= "host") に claude CLI install
sudo apt install -y nodejs npm
sudo npm install -g @anthropic-ai/claude-code   # → 2.1.138

# host 側で fake session JSONL 作成 (auth なしでも作れる)
PROJ=/tmp/poc-claude-test
KEY=-tmp-poc-claude-test
SESSION=11111111-2222-3333-4444-555555555555
mkdir -p ~/.claude/projects/$KEY
cat > ~/.claude/projects/$KEY/$SESSION.jsonl <<EOF
{"type":"summary","summary":"Test resume session","leafUuid":"$SESSION"}
{"type":"user","sessionId":"$SESSION","cwd":"$PROJ","message":{"role":"user","content":[{"type":"text","text":"hello from host"}]},"uuid":"$SESSION","timestamp":"..."}
EOF

# container 起動 + bind-mount 2 個
lxc launch ubuntu:24.04 poc-resume
lxc config set poc-resume raw.idmap "both 1000 1000"
lxc restart poc-resume
lxc config device add poc-resume claude_dir disk source=/home/ubuntu/.claude path=/home/ubuntu/.claude
lxc config device add poc-resume claude_json disk source=/home/ubuntu/.claude.json path=/home/ubuntu/.claude.json   # ← 重要

# container に claude install
lxc exec poc-resume -- bash -c "apt install -y nodejs npm && npm install -g @anthropic-ai/claude-code"

# container で resume 実行
lxc exec poc-resume -- sudo -u ubuntu bash -c \
  "cd $PROJ && timeout 10 claude --resume $SESSION --print test < /dev/null"
```

### 結果

| 試行 | 出力 | 解釈 |
|---|---|---|
| host: `claude --resume $SESSION --print x` | `Not logged in · Please run /login` | session JSONL 発見 + parse 成功 + API call 段階で auth wall |
| container: bind は `~/.claude/` のみ | `Claude configuration file not found at: /home/ubuntu/.claude.json` | **新発見**: claude CLI は `~/.claude.json` (file) も要求 |
| container: bind に `~/.claude.json` 追加 | `Not logged in · Please run /login` | **host と完全に同一の挙動** = resume 機構が container でも機能 |

### Decision (Sdd4ce1 への申し送り)

**bind-mount 戦略を更新** — 以下 **2 つの bind が必須**:

1. `lxc config device add <inst> claude_dir disk source=$HOME/.claude path=$HOME/.claude` (rw)
2. `lxc config device add <inst> claude_json disk source=$HOME/.claude.json path=$HOME/.claude.json` (rw or ro)

`~/.claude/` ディレクトリだけでは不十分 — claude CLI は `~/.claude.json` (config file, OAuth token を含む) を **別途** 開く。 これを bind しないと container 内 claude は起動段階でエラー。

→ **Sdd4ce1 AC-3-1 を更新**: bind device 追加を 2 つに明記。 ROADMAP.json で対応。

### Auth scope の話

container 内 claude が API 呼び出しまで到達できるかは VM の auth 状態に依存する (host VM は auth しなかったので両方 "Not logged in")。 本 PoC は **bind-mount + idmap + resume infrastructure が機能する** ことの実証であり、 OAuth flow 自体の検証ではない。 OAuth credentials を bind 越しに container 内 claude が使えるかは Sdd4ce1 で別途検証 (host で auth → 同 `~/.claude.json` を bind → container claude が auth 状態を継承するはず)。

---

## 詳細実行ログ

### Setup: LXD 初期化

`lxd init --auto` を VM 上で実行してストレージプール (dir driver) とブリッジネットワーク (lxdbr0) を設定。

### AC-S98156b-2-1: bind-mount 確認

```
Container: poc-claude (ubuntu:24.04)
raw.idmap: "both 1000 1000"
Device: home disk source=/home/ubuntu path=/home/ubuntu

lxc exec poc-claude -- ls -la /home/ubuntu/.claude/
total 12
drwxrwxr-x 3 ubuntu ubuntu 4096 ... .
drwxr-x--- 9 ubuntu ubuntu 4096 ... ..
drwxrwxr-x 2 ubuntu ubuntu 4096 ... skills
```

**結果**: `~/.claude` の内容が container 内から可視。所有者が `ubuntu:ubuntu` と正しく表示される。

**Note**: `raw.idmap "both 1000 1000"` (2引数形式) を設定後に restart。  
host uid=1000 が container uid=1000 にマッピングされることで、ディレクトリの所有者表示が一致した。

### AC-S98156b-2-2: session 参照 (bind-mount 双方向確認)

```
claude CLI not found on host — verifying bind-mount path integrity as proxy
echo '{"session_id":"test-abc123"}' > ~/.claude/test-session.json
lxc exec poc-claude -- cat /home/ubuntu/.claude/test-session.json → "test-abc123" visible

PASS: bind-mount is bidirectional
```

**Note**: claude CLI は test VM 未インストール。bind-mount の双方向性 (= same inode) を file I/O で確認。  
`claude --resume <session-id>` が機能するためのインフラ (同一 path / 同一 inode の bind) は成立している。

### AC-S98156b-2-3: container → host 書き込み

```
lxc exec poc-claude -- bash -c "echo 'container-wrote-this' > '~/.claude/container-write-test.txt'"
cat ~/.claude/container-write-test.txt → "container-wrote-this" (即座に表示)
File owner uid on host: 1000000
```

**結果**: 双方向 bind が機能している。ただし **uid=1000000** が重要な発見。

**UID 発見**: `raw.idmap "both 1000 1000"` 設定後でも、container の root (uid=0) が host 上では uid=1000000 で見える。  
これは LXD のデフォルト subordinate UID shift (65536→1065536 の範囲) による。  
container 内の ubuntu user (uid=1000) が host から uid=1000000+1000=1001000 等に見える可能性がある。

後続の `poc-a-correct` 実験で `raw.idmap "uid 1000 1000\ngid 1000 1000"` (改行区切り) 形式を検証した。

### AC-S98156b-2-4: settings.json 戦略

```
settings.json not found on host → no conflict
```

**Decision 確定**: `~/.claude/settings.json` の bind 戦略を以下に決定:

**採用戦略**: **ro (read-only) bind + palmux による container 専用 settings.json inject**

| 戦略 | メリット | リスク |
|---|---|---|
| Full bind (rw) | シンプル | host/container の always-allow, MCP servers が混在 |
| **ro bind + inject** (採用) | container 専用設定を palmux が管理 | palmux が settings 書き込みを担当 |
| 完全分離 (bind なし) | 最も安全 | session history が共有されない (resume 不可) |

**根拠**: claude CLI の `--resume` 機能はセッション JSONL を `~/.claude/projects/<path>/sessions/` に書くため、この部分は rw bind が必要。  
settings.json は palmux が container open 時に書き込むことで host 設定との干渉を防ぐ。

### AC-S98156b-2-5: 同時書き込み (concurrent write)

```
host 5回 + container 5回 concurrent append → 10/10 行生存

{"source":"host","seq":1}
{"source":"host","seq":2}
{"source":"host","seq":3}
{"source":"container","seq":1}
{"source":"host","seq":4}
{"source":"container","seq":1}  ← seq重複あり (container側の競合)
{"source":"host","seq":5}
...
```

**結果**: 10行生存 → PASS だが、container 側の seq が重複 (container 内での write race)。  
JSONL へのアトミックな append は 1 行が小さければ kernel が direct write で直列化する。  
実運用では host palmux と container claude が同一 JSONL を **同時書きしないことを契約** (§4.4 の「二重起動禁止」)。

**§4.4 二重起動禁止の根拠**: kernel の append は競合時に行が断片化する可能性がある。  
palmux は Workspace open 時にフラグファイルを置いて二重起動を検出する (Phase A で実装)。

---

## Decisions

### D-POC-A-1: raw.idmap の正しいフォーマット

**調査結果**: LXD 5.21.4 では `raw.idmap` の形式は以下:
- `"both 1000 1000"` (3引数) — uid と gid 両方を 1000→1000 にマップ (single-range で count=1)
- `"uid 1000 1000"` + `"gid 1000 1000"` (改行区切り) — 同義

`"both 1000 1000 1"` (4引数 = type host-id container-id count) は LXD 5.21.4 で **Invalid** エラーとなる。

**実測UID**: raw.idmap 設定後でも container の exec は root (0) で実行。  
container ubuntu user (1000) が host で 1000 として見えるかは bind-mount の permissions と chown に依存。

### D-POC-A-2: claude --resume 機能要件

**bind 設定**:
```
lxc config device add <container> home_claude disk \
  source=/home/ubuntu/.claude \
  path=/home/ubuntu/.claude
```
+ `raw.idmap "uid 1000 1000\ngid 1000 1000"` (container ubuntu が host ubuntu と同 uid で read/write)

**セッション継続確認**: container 内で `claude --resume <session-id>` が機能するためには:
1. `~/.claude/projects/<project-path>/sessions/<session-id>.jsonl` が bind 越しに読める ✓
2. container 内 claude が同 JSONL に追記できる ✓ (rw bind の場合)
3. container 内 claude の UID が JSONL owner と一致する ← **idmap が key**

### D-POC-A-3: settings.json 戦略 (最終決定)

- `~/.claude/settings.json` は palmux が container open 時に書き込む専用 settings を inject
- session JSONL は rw bind で共有 (resume 機能に必須)
- `~/.claude/skills/` は ro bind (host skills を container 内 claude が参照できる)

---

## Phase A への申し送り

1. **idmap 設定の正確な形式**: `raw.idmap "uid 1000 1000\ngid 1000 1000"` を使う。`both 1000 1000` は動くが uid/gid 個別指定の方が明示的。
2. **ubuntu user の作成待ち**: container 起動直後は ubuntu user が作成されていない場合がある (cloud-init が完了するまで)。palmux が Workspace open で container start 後に `lxc exec -- id ubuntu` でポーリング待機が必要。
3. **bind-mount の path 設計**: `~/.claude/` 全体を bind するのではなく、以下を分離:
   - `projects/` → rw bind (session JSONL の共有に必須)
   - `skills/` → ro bind (host skills 参照)  
   - `settings.json` → palmux が inject (container open 時に write)
   - その他 (`todos/`, `statsig/` 等) → ro bind or 分離
4. **二重起動防止**: Workspace open 時に `~/.claude/projects/<path>/.palmux-lock` を作成、Workspace close で削除。container 内 claude start 前にチェック。
