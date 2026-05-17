# SDK Credit Analysis — Sprint S1d2278-5

**作成日**: 2026-05-17  
**対象プラン**: Max 20x ($200/月)  
**調査目的**: 2026-06-15 以降の Agent SDK クレジット分離後の月次コスト見通しを確定し、Track B 続行の経済的妥当性を判定する

---

## 1. Anthropic 公式情報

### 参照 URL

| URL | 取得結果 |
|-----|----------|
| https://support.claude.com/en/articles/15036540-use-the-claude-agent-sdk-with-your-claude-plan | **$200 / 月**のクレジット (Max 20x)。月次リセット、未使用分は繰越不可 |
| https://claude.com/pricing | Max 20x = $200/月。Agent SDK クレジット額の記載なし（support ページ参照） |
| https://platform.claude.com/docs/en/docs/about-claude/models | モデル料金レートカード取得成功（下記参照） |

### Agent SDK クレジット (Max 20x)

| 項目 | 値 |
|------|----|
| **月次クレジット額** | **USD $200** |
| **リフレッシュ単位** | 月次（billing cycle 開始時にリセット） |
| **未使用クレジットの繰越** | **なし**（"Unused credits don't roll over to the next billing cycle"） |
| **クレジットの共有** | **不可**（"Credits belong to individual accounts. They can't be shared or pooled across teammates."） |
| **超過時の課金** | 標準 API レートで従量課金（"additional Agent SDK usage flows to extra usage at standard API rates"） |

### 現行 API レートカード（超過時適用）

| モデル | Input ($/MTok) | Output ($/MTok) | 備考 |
|--------|---------------|-----------------|------|
| **Claude Opus 4.7** | $5 | $25 | 現行最新 Opus |
| **Claude Opus 4.6** | $5 | $25 | |
| **Claude Sonnet 4.6** | $3 | $15 | |
| **Claude Haiku 4.5** | $1 | $5 | |

> sessions.json に記録されている主力モデル `claude-opus-4-7[1m]` は **Opus 4.7 (1M context)** に相当し、input $5 / output $25 per MTok が適用される。

---

## 2. 現状の月次実績推定

### データソース

1. **`/tmp/sessions.json`** (palmux2 dev サーバーが管理する claude-agent セッション記録)
   - フィールド: `totalCostUsd` — palmux2 claude-agent タブが累積する実コスト
2. **`~/.claude/projects/**/*.jsonl`** (Claude Code トランスクリプト)
   - 直近 30 日 (495 ファイル) を全件走査。JSONL の各行に `costUSD` / `cost` / `total_cost_usd` フィールドは**存在しない**（Claude Code の JSONL はトークン使用量を記録しているが、USD コストフィールドは含まれていない）。よってこのソースからの集計は**不可能**。

### sessions.json 集計結果

調査実施日時点 (2026-05-17) の `tmp/sessions.json` から直近 30 日（2026-04-17 〜 2026-05-17）を対象に集計：

| 指標 | 値 |
|------|----|
| **対象期間内セッション数** | 62 件 |
| **うち totalCostUsd > 0 のセッション数** | 39 件 |
| **直近 30 日合計コスト** | **$98.96** |
| **最大単一セッションコスト** | $40.93 |
| **平均コスト (非ゼロセッション)** | $2.54 |
| **主力モデル** | claude-opus-4-7[1m] (全コストの 98.9%) |
| **データ期間** | 2026-04-27 〜 2026-05-11 |

> **注意**: sessions.json のデータは 2026-04-27 以降のみ存在（それ以前は蓄積なし）。実際の 30 日窓は 約 2 週間分のデータで覆われている。観測データの月換算額は $98.96 だが、これはほぼ 2 週間のデータに基づく推定であるため、月次換算では **$150〜$200 以上に達する可能性**が高い。

### jq 相当クエリ（参考）

```bash
jq '[.sessions[] | select(.totalCostUsd != null and .totalCostUsd > 0) | .totalCostUsd] | add' \
  tmp/sessions.json
# => 98.9584
```

---

## 3. 判定

### 判定: `overage`

| 比較軸 | 値 |
|--------|----|
| Max 20x Agent SDK 月次クレジット | $200 |
| 実績 (約 2 週間分) | $98.96 |
| 月次換算推定 (×2) | **~$198〜$210** |
| クレジット超過額 (推定) | **$0〜$10 / 月** (低端) 〜 **$50〜$100 / 月** (高端) |

- 現状の約 2 週間のデータでは実績がクレジット上限 ($200) にほぼ到達している。
- 月の後半や sprint 集中期にスパイク ($40 超のセッション) が発生した場合、容易に上限を超過する。
- 超過倍率は最大 ~1.5× 程度と推定されるため、判定は **`overage`** (2× 未満の超過) とする。
- ただし sprint 集中期が重なると **`large-overage`** に転ずるリスクがある。

### 追加月次コスト推定 (Track A 継続の場合)

| シナリオ | 追加課金 (USD/月) |
|----------|-------------------|
| 通常月 (超過なし) | $0 |
| 通常月 (軽微超過 ~$20) | $20 |
| sprint 集中月 (超過 ~$50〜100) | $50〜$100 |
| 最悪ケース (スパイク複数) | $100〜$150 |

---

## 4. Track B 続行の経済的妥当性 (定性)

Track A (claude-agent SDK モード) を継続する場合、2026-06-15 以降は Agent SDK クレジット ($200/月) を消費し尽くした時点で Anthropic 標準 API 料金 (Opus 4.7: $5 input / $25 output per MTok) での従量課金が発生する。現状の使用パターンでは月次実績がクレジット上限にほぼ到達しており、sprint 集中期には $50〜$100 超の追加課金が予見される。一方 Track B (Go 管理 PTY 経由のインタラクティブ claude) は Max サブスクリプション定額の quota 内で動作するため、追加課金は原則ゼロとなる。Max 20x プランのコストが $200/月 (年間 $2,400) であることを踏まえると、Track B で月 $50〜$100 の API 課金を回避できれば **年間 $600〜$1,200 相当の節約**となり、実装コストを十分に上回る経済的メリットが見込まれる。ただし Track B の PTY 実装は複雑度が増すため、技術的トレードオフと合わせて最終判断を行うことを推奨する。

---

## 5. 補足 / 不確実性

- `~/.claude/projects/` の JSONL にコスト情報がないため、Claude Code (コーディングエージェント) 側の cost は別途 Anthropic コンソールで確認が必要。
- セッションデータが 2 週間分しかないため、月次換算は推計値である。正確な把握には 1 billing cycle 分のデータ蓄積が必要。
- Anthropic の課金詳細は support.claude.com に記載があるが、プラン別の詳細 (Max 5x の credit 額など) は現時点では "unknown — needs Anthropic support clarification" の可能性を残す。Max 20x については **$200/月** と明示されていることを確認済み。
