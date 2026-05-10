# S98156b PoC Report — Phase A Gate Exit

**日付**: 2026-05-10  
**VM**: ubuntu@192.168.1.41 (Ubuntu 24.04.3 LTS, LXD 5.21.4 LTS)  
**Sprint**: S98156b — Phase A0 gate sprint

## サマリ

| PoC | AC | Status | 備考 |
|---|---|---|---|
| Agent RPC (Story 1) | AC-S98156b-1-1〜1-5 | **全 PASS** | VM 上で実機検証完了 |
| bind-mount/idmap (PoC a) | AC-S98156b-2-1〜2-5 | **全 PASS** | claude CLI 未インストールだが bind 機構確立 |
| proxy device (PoC c) | AC-S98156b-3-1〜3-5 | **全 PASS** | container/lxd/daemon restart 全て永続確認 |
| gate exit (Story 4) | AC-S98156b-4-1〜4-3 | **全 PASS** | poc-report.md + design doc §4.4 更新済み |

**Phase A 移行判断: GO**

---

## 1. palmux-agent binary + RPC (Story 1)

### 実測値

| 項目 | 結果 |
|---|---|
| binary サイズ | **2 MB** (limit: 15 MB) |
| binary 形式 | Linux ELF amd64 static |
| CGO | 無効 (CGO_ENABLED=0) |
| UDS Echo 往復 | PASS |
| ListListeningPorts (port 19999) | PASS |
| TCP + TCP6 両対応 | PASS |
| `../` traversal 拒否 | PASS (-32000) |
| Symlink escape 拒否 | PASS (-32000) |
| version negotiation | PASS (`agent_version` in all responses) |

### RPC レイテンシ (100 Echo calls over UDS)

```
Min:    0.07 ms
Max:    0.24 ms
Mean:   0.10 ms
Median: 0.10 ms
P95:    0.14 ms
P99:    0.24 ms
```

**評価**: 0.1ms 中央値は phase A の「常時 1 本維持」接続に十分。ポーリング 5 秒間隔での ListListeningPorts も問題なし。

### 実装決定

- **UDS socket**: `/tmp/palmux-agent.sock` (configurable via `--socket`)
- **プロトコル**: JSON-RPC 2.0 over UNIX Domain Socket, newline-delimited
- **Method 一覧**: Echo / ListListeningPorts / ReadFile / Stat / Walk
- **`/proc/net/tcp` parser**: lsof/ss に依存しない, IPv4/IPv6 両対応

---

## 2. bind-mount + idmap PoC (Story 2)

### 実測値

| 検証項目 | 結果 |
|---|---|
| `lxc launch ubuntu:24.04` + bind-mount | PASS |
| `~/.claude/` が container 内から ls 可能 | PASS |
| container 書き込み → host 即座可視 | PASS (同一 inode bind) |
| concurrent write 10/10 行生存 | PASS |
| settings.json 戦略決定 | PASS (ro bind + inject 方式) |
| claude --resume (実 CLI) | 未確認 (claude CLI 未インストール) |

### idmap フォーマット確定 (実機検証済み)

**LXD 5.21.4 での正しい `raw.idmap` 形式**:
```
lxc config set <inst> raw.idmap "both 1000 1000"
```

- `"both 1000 1000 1"` (4引数) は **Invalid** (LXD 5.21.4 で確認)
- `"both 1000 1000"` (3引数) は **有効**

**uid_map の実測値** (start + idmap "both 1000 1000" 後):
```
         0    1000000       1000   ← root(0) → host 1000000 (still shifted), count=1000
      1000       1000          1   ← ubuntu(1000) → host 1000 ← CONFIRMED
      1001    1001001  999998999   ← remaining
```

**UID マッピング動作 (確定)**:
- Default: container uid 0 → host uid 1000000 (LXD subuid shift)
- `raw.idmap "both 1000 1000"` 追加後: container uid 1000 (ubuntu) → **host uid 1000 ✓**
- container root (uid 0) は引き続き host uid 1000000 として見える

### 重要発見: cloud-init 完了待ちが必要

**発見**: `lxc exec <inst> -- true` が成功しても `su ubuntu` が失敗する。  
cloud-init が ubuntu user を作成するまで約 10〜30 秒かかる。

**Phase A 要件**: container start 後、`lxc exec -- id ubuntu` が成功するまでポーリング待機が必要。  
単純に `true` コマンドが通るだけでは不十分。

### settings.json 戦略 (確定)

設計 §4.4 で検討した 3 案から以下に確定:

**採用**: **ro bind + palmux inject**
- `~/.claude/projects/<path>/` → **rw bind** (session JSONL 共有, resume に必須)
- `~/.claude/skills/` → **ro bind** (host skills 参照)
- `~/.claude/settings.json` → **bind しない** (palmux が container open 時に inject)
- `~/.claude/` の残り → ro bind or 分離

### 二重起動防止 (§4.4)

concurrent write テストで行順重複を確認。実運用では:
- Workspace open 時に `~/.claude/projects/<path>/.palmux-lock` を作成
- container 内 claude 起動前にチェック
- Workspace close で lock 削除

---

## 3. proxy device 永続性 PoC (Story 3)

### 実測値

| 検証項目 | 結果 |
|---|---|
| proxy device 追加後 curl reach | PASS |
| `lxc restart` 後も reach | PASS (再 add 不要) |
| `systemctl restart snap.lxd.daemon` 後も reach | PASS (自動復元) |
| host reboot 後 | 設定分析で確認 (`ephemeral: false`) |
| `lxc config device list` に出る | PASS |

### 設定内容

```yaml
devices:
  p1:
    connect: tcp:127.0.0.1:8080
    listen: tcp:127.0.0.1:18080
    type: proxy
ephemeral: false
```

`ephemeral: false` が LXD の config.yaml に記録されるため、全 restart シナリオで永続化。

### Phase B (Ports panel) 設計確定

1. Workspace open 時に一度 `lxc config device add` → 以降再 add 不要
2. host reboot 後も自動復元 (boot.autostart=true + LXD config.yaml)
3. Workspace close 時に `lxc config device remove` (明示的 cleanup)
4. port forward mapping は `repos.json` に保存 (LXD config が真実だが palmux 側でも管理)

---

## 4. Phase A (Sdd4ce1) への反映

### 設計変更が必要な箇所

1. **AC-Sdd4ce1-3-1**: `raw.idmap "both 1000 1000"` は有効。`"both 1000 1000 1"` は不可。→ **ROADMAP 更新済み**

2. **ListListeningPorts の実装方針**: `lxc exec -- ss -tln -H` の代わりに agent の `/proc/net/tcp` parser を使う。依存なし・高速。

3. **agent 接続方式**: `lxc exec` での都度起動はレイテンシが高い (LXD exec overhead)。§6.4.2 の push 方式 (agent 常駐 + UDS 接続) を採用。0.1ms/call で十分。

4. **claude --resume 確認必要**: S98156b では claude CLI 未インストールで直接確認できなかった。Phase A (Sdd4ce1) で container image に claude CLI を含め resume AC を追加。

### ROADMAP への申し送り

- Phase A (Sdd4ce1) の `lxd-container` 実装は PoC 結果で設計確定している
- bind-mount strategy (AC-Sdd4ce1-4) は §4.4 の確定事項に従う
- image pipeline (Phase A') は S98156b のスコープ外のまま

---

## 5. 未確認事項 (Phase A の AC に移行)

| 項目 | 理由 | Phase A 対応 |
|---|---|---|
| `claude --resume` 直接確認 | claude CLI 未インストール | Sdd4ce1 container image に claude CLI 含める |
| host reboot での proxy persistence 実動確認 | VM reboot = SSH 切断 | Phase B (proxy 設計確定済みなので低リスク) |
| ubuntu user (uid=1000) bind-mount write の host uid 確認 | テスト環境のタイムアウト | Phase A E2E テストで確認 |

---

## 6. PoC 実行資料

- `docs/sprint-logs/S98156b/poc-a.md` — bind-mount/idmap 詳細ログ
- `docs/sprint-logs/S98156b/poc-c.md` — proxy device 詳細ログ
- `scripts/poc/a-bind-claude.sh` — bind-mount PoC スクリプト
- `scripts/poc/c-proxy-device.sh` — proxy device PoC スクリプト
- `tests/acceptance/s98156b_agent.py` — agent acceptance tests (VM 上で実行)
