# Comprehension Report — no-halt-agent milestone (Sprints S3f2658 + S862203)

_Generated at milestone arrival. Read this before `autopilot review`._

## How to run it

これは全部 **バックエンド/インフラ** の変更で、新しい UI 画面はありません。体感する一番速い経路:

1. `make serve INSTANCE=dev` で dev インスタンスを起動（ホスト用 palmux2 は触らない — 開発の鉄則）。ブラウザで表示された dev URL を開く。
2. claude-tui タブ（または claude-agent タブ）で claude を動かし、何か作業させる。
3. 別シェルで **dev インスタンスの palmux2 プロセスだけ**を再起動する（`systemctl --user restart` した transient unit か、`make serve INSTANCE=dev` の再実行）。**ホスト palmux2 は再起動しない**（自分の Claude セッションを巻き込むため）。
4. ブラウザを再接続 → claude の画面（tui）／会話・権限状態（agent）が**再起動を跨いで復元**されていることを確認。

自動検証の証跡: `docs/sprint-logs/S3f2658/e2e-S3f2658-2.json`（tui 復元）, `docs/sprint-logs/S862203/e2e-S862203-3.json`（agent 復元、スクショ付き）。

## What changed

- **palmux2 を再起動しても、走っている claude が死ななくなった。** 従来は self-update / `systemctl restart` / クラッシュのたびに実行中の claude(tui・agent 両方)が巻き添えで殺されていた。今は claude を palmux2 とは別 cgroup の `palmux ptyhost` プロセスが保持し、palmux2 は unix socket で再接続する。
- **claude-tui タブ**: 再起動後にタブを開くと、走っていた claude の**画面がそのまま復元**される（再起動中に出続けた出力も欠落しない）。
- **claude-agent タブ**: 再起動を跨いで**会話 transcript が無欠損で復元**され、**再起動中に来た権限要求(permission)も UI に届いて応答でき、ツール実行が成立**する。stream-json イベントは絶対 offset 付き行 ring に貯めて ACK/replay でロスレス再接続する。
- **incus workspace でも同じ生存性**: コンテナ内 claude が palmux2 再起動を跨いで同一 pid で生存し、タブを閉じると確実に reap される。
- **付随して直った既存バグ 2 件**: (1) agent タブがそもそも**どんな再起動でも resume できていなかった**（`SetLastInit` が毎ターン migration guard を潰し、次回起動時に resume ポインタを孤児として捨てていた）。(2) 起動時の shutdown 経路が `Shutdown()`（全 agent kill）を呼んでいて `DetachAll` にすべきだった。

## Why this way

- **detached ptyhost + thin holder（ADR-0001/0002）**: claude を「palmux2 から独立した薄いプロセス」に持たせ、palmux2 はミラー＋入力中継に徹する（priority_rule 1「CLI が真実」と一致）。ptyhost は claude/incus/tmux を一切知らない汎用プロセスホルダに保った（却下: palmux2 内で claude を持ったまま再起動を耐える設計 — cgroup 道連れが構造的に避けられない）。
- **cgroup 脱出は systemd-run --scope、無ければ setsid fallback（ADR-0003）**: NixOS アプライアンス等 systemd 環境では scope で cgroup 分離、非 systemd/D-Bus 不在では setsid+double-fork にランタイム fallback。
- **agent はロスレス offset replay（ADR-0004）**: stream-json は 1 行も落とせない（transcript 破損・宙に浮く permission になる）ので、絶対 offset 行 ring + ACK/last-ack replay。冒頭 spike で**実 claude が遅延した permission 応答を最大 60s まで受理する**ことを確認してから設計を確定（deadline < 再起動時間なら設計見直しの gate だった → クリア）。
- **transcript 復元は CLI の .jsonl backfill + replay の合わせ技**: 復元画面の「実行中ターン先頭〜pending permission 直前」は claude 自身の `.jsonl`（真実の所在）から、そこから先は replay から。priority_rule 1 に忠実。
- **重い実機テストは env-gate（opt-in smoke）**: 既存の `PALMUX_REALINCUS_SMOKE` 慣習に合わせ、実 systemd/実 claude/並走テストを default `go test` から外した（default が緑・プロセスリークなし・隣接テストの CPU 競合 flake を排除）。verify では明示フラグで実行し全 PASS。

## What to verify

自動テスト＋独立 verifier は両 Sprint とも `agree_sprint_complete`。人の目で見る価値がある点:

- **⚠ 実機再起動の体感（最重要）**: 上の「How to run it」を実際にやり、tui 画面復元・agent 会話+権限復元が**あなたの環境で**期待通りか。特に「再起動中に来た permission に後から答えてツールが動く」フローは実 claude E2E で PASS 済みだが、体感確認の価値が高い。
- **incus workspace の生存 (AC-3f2658-4)**: dev box の実 incus で PASS x2 済みだが verify では再実行していない（compromises.json 参照）。別の incus ホストで独立再確認したい場合は手動 smoke を。
- **並行 permission の復元 (RD-26)**: claude が並列ツール呼び出しで複数 permission を同時に出したまま再起動 → 未応答のものだけ再浮上・応答済みは再浮上しない、を verifier がコードで確認済み。実運用で複数 permission を跨ぐケースがあれば一度目視を。
- backlog に積んだ 7 件（pre-existing race 2 件、claudeagent の orphan-GC 欠如、test-hygiene リーク 等）— いずれも本 Sprint の AC 外の既存 or 派生課題。詳細は下記。

## What was assumed

- **claude の permission 応答 deadline は再起動時間より十分長い**（spike で ~60s まで確認、それ以上は未測定）。将来 claude CLI が短い deadline を導入したら ADR-0004 replay 設計の再検討が要る。
- **ring 溢れ = ロスレス復元不能 → 新規セッション扱い**を許容する仮定（長時間切断時）。溢れは黙って欠けた transcript を出さず明示シグナルにしてある。
- **claude の `.jsonl` が真実の transcript ソース**として読める前提（読めなければ replay-only に安全 fallback）。
- **claudeagent には orphan-GC がまだ無い**（claudetui は S3f2658-3 で実装済）: palmux2 停止中に branch/worktree を消すと agent ptyhost が reap されず残る（再オープンで self-heal）。backlog 済み、AC 外。
- 重い実機テストの env-gate は「verify で明示実行する」運用前提。CI で回すなら gated ジョブ追加が要る。
