# Comprehension Report — Sd44947 (共有フォルダの宣言化 profile-as-mold + 実行時反映)

_Generated at milestone arrival. Read this before `autopilot review`._

## What changed

- **Workspace コンテナへの共有 device が「宣言（incus profile）」になった。** これまで palmux は各コンテナに個別に device を add していた（ghq / .claude / dotfiles / 認証 / hook binary）。今後は単一の incus profile `palmux-shared` にそれらを集約し、コンテナは `default + palmux-shared` の 2 profile で起動する。profile が金型（mold）で、コンテナはそこから成形される。
- **宣言と実態のドリフトが毎スキャンで自己修復する。** 既存の 10 秒スキャンループに profile の reconcile が相乗りした。誰かが profile から device を手で消しても、次のスキャン（〜16 秒以内）で宣言どおりに戻る。See8bd4 の公開 route self-heal と同じ発想。
- **旧コンテナが無停止で新方式に移行する。** 起動時に per-container device を profile へ移し替える（profile add + 旧 device remove）。走行中の claude / ghq / gh 認証はそのまま。
- **ユーザが GUI/config から共有フォルダを足せるようになった。** `config.toml` の `[workspace] shared_dirs = ["~/.infisical", ...]` と、設定パネル「デプロイ」タブの新「共有フォルダ」セクション（一覧 / 追加 / 削除 / ⚠ 警告）から、`~/.claude` と同じ性質の共有を **palmux のコード変更・リリースなしに** 追加できる。Apply すると profile を書き換え、**稼働中のコンテナにも live で device add/remove が伝播** する（palmux2 再起動なし）。
- **パスは `$HOME` 配下に制限される。** GUI 側でも server 側でも `$HOME` スコープを検証し、範囲外は 400 + インラインエラー。source 不在のパスはエラーにせず skip。

## Why this way

- **profile-as-mold を採用**（却下: per-container device add の継続）。宣言と稼働状態を突き合わせて修復できる自然な単位が incus の profile であり、ドリフト検出・修復が「ただ宣言に収束させる」だけで済む（DESIGN_PRINCIPLES priority_rule 6「既存資産活用」— incus native の profile を使い車輪を再発明しない）。
- **共有フォルダの Apply を「即時（hot）」分類に**（却下: 要再起動）。既存の設定 Apply 分類（hot/restart/root）に `workspace` クラスを足し、profile 書換 + 稼働コンテナ伝播を in-process で行う。ユーザ操作 → 即反映で、走行中の claude を落とさない（priority_rule 4「lazy/非破壊」の精神）。
- **⚠ 露出警告を常時表示**（却下: 追加時のみ）。共有フォルダは中の claude/シェルから読み書き可能になる責務越境。`~/.claude` 共有と同じ明示同意の思想（priority_rule 5「責務越境最小」）。
- **GUI の Apply testid は既存の `apply-deploy` を流用**（却下: gui-spec の `deploy-apply`）。既存の testid/API 契約を壊さない（priority_rule 「パッチ間互換」）。gui-spec 側を実コードに合わせた。
- **AC-2-5 の transient/fault 状態は mock テストで検証**（却下: 実バックエンド E2E に畳む）。Loading（GET 遅延）と SaveError（apply 500）は実バックエンドでは決定論的に起こせないため、Playwright `page.route` で mock する専用テストに分けた。
- （DESIGN/ADR は本プロジェクトに無し。設計原則は `docs/palmux2-nixos-appliance-design.md` §5–§8 の profile-as-mold + 2フェーズ適用 に準拠。）

## What to verify

- **⚠ (推奨・belt-and-suspenders) deploy-test 192.168.1.43 の実 production での通し確認。** 実 incus smoke は **dev ボックスの実 incus**（新バイナリ + 使い捨て probe コンテナ + live dev インスタンスの reconcile）で PASS 済みだが、192.168.1.43 は **旧バイナリが稼働中** のため未実施。AC 自体は実モードで満たされている（skip ではない）が、本番相当ホストでの `profile show` / device 削除→復活 / 旧コンテナ移行 / `~/.infisical` の live add→remove を一度目視するのが安全。sprint の `review_reason`（priority_rule 9 の通常 real-mode smoke）に対応。
- **共有フォルダのセキュリティ・トレードオフ（設計上の既知事項）。** 追加したフォルダは全 Workspace コンテナの claude/シェルから読み書き可能になる。`~/.claude` 共有・S5818e8 の SSH 鍵/gh トークン共有と同じ「フル機能・再認証なし」前提。`~/.infisical` のような認証フォルダを足す = その秘密を全コンテナに露出する、という点をユーザが意図して使う想定。GUI の ⚠ 警告がそれを明示している。
- **実際のブラウザでのデプロイタブ操作感**（refine で目視推奨）。承認済みプロトタイプ `prototype/sd44947-shared-folders.html` に一致させたが、実 UI の余白・トースト・エラー表示は人の目で。
- （verifier の overlooked / fail 項目は残っていない: 最終 verification-report は overall=pass, ac_warnings 0, overlooked 0。）

## What was assumed

- **`palmux-shared` という固定 profile 名を単一 palmux インスタンスが占有する**と仮定。同一ホストで複数 palmux インスタンス（別 `--tmux-prefix`）が incus を共用するケースは想定していない（VISION: シングルユーザ・自前ホスティング前提なので通常は問題にならない）。
- **共有フォルダのライブ伝播は「稼働中の全 incus コンテナ」に対して行う**と仮定。特定 Workspace だけに共有する粒度は用意していない（全コンテナ一律。要件が出たら profile 分割で対応可能）。
- **`$HOME` スコープ検証はホストの `$HOME`（例 `/home/ubuntu`）基準**と仮定。GUI は GET /api/deploy が返す host home を使って client 側でも同じ規則で弾く（server と二重防御）。
- **finalization は autopilot が代行した。** sprint agent が最終 commit 前に落ちたため、autopilot が 6-guard 再評価 + 機械判定（verify-run.json overall=pass）確認の上で commit/merge した。実装・テストは agent が書いたもの、AC-2-5 の mock テストのみ autopilot が finalization で追記（`compromises.json` の scope_changes 参照）。
