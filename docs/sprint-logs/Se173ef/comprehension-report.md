# Comprehension Report — palmuxOS 更新 UX + アプリカード + Sprint タブ刷新 (Sprints S673a42, S41bdf2, Se173ef)

_Generated at milestone arrival. Read this before `autopilot review`._

## What changed

- **palmuxOS アプライアンスで「更新」を GUI からキックできるようになった (S673a42)。** これまで NixOS ホストの更新パネルは案内文だけで、ユーザは SSH で `nix flake update palmux && nixos-rebuild switch` を手打ちしていた。いま更新パネルに更新ボタンが出て、押すと本体 (nixos-rebuild) と palmux-ws イメージの更新をブラウザから実行でき、完了後は自動再接続してトースト表示する。世代アトミック切替なので失敗しても旧世代が残り、ロールバック導線を案内する。案内文の flake パスバグ (`/etc/palmux#appliance` → 実体 `/persist/palmux/nixos#appliance`) も直り、パスはバックエンドが返す単一 source になった。

- **設定に「アプリ」セクションが増え、1アプリ=1カードで CLI ツールを導入・共有できる (S41bdf2)。** 各カードにトグルが2つ: 「インストール」(例 infisical) と「認証フォルダを共有」(~/.infisical)。**インストールはホストと全コンテナに同時に行き渡る** — 構造化データ (`apps.json`) から `environment.systemPackages` の drop-in を一方向生成し、nixos-rebuild で共有 `/nix/store` 経由で全コンテナ+ホストに届く。共有トグルはインストールに従属 (未導入ならグレーアウト)、Sd44947 の shared_dirs 金型を再利用して稼働中コンテナへ hot 反映する。カタログ (infisical/1password-cli/gh/awscli2) に加え、任意の nixpkgs 名を `nix eval` 検証付きで追加できる。

- **Sprint タブが現行の sprint/autopilot skill の成果物を読むようになった (Se173ef)。** これまでタブは旧 artifact (acceptance-matrix.json / e2e-results.json 等) を読んでおり、現行 skill が書く trust-source (機械判定 verify-run / 独立 verifier report / 6-guard done-judgment / compromises / comprehension-report / gui-spec) を一つも表示できず、該当セクションが空だった。いまはそれらを全部パースし、trust-source を最上段に置く情報設計 (機械判定+verifier findings → 6-guard grid → compromises 重大度別 → comprehension Markdown → gui-spec state_diagram を mermaid) に刷新された。**このレポートを表示しているのが、まさにその新しい Sprint タブ。**

## Why this way

- **更新は既存の GUI キック nixos-rebuild 経路を拡張して流用 (S673a42)** — 版数更新用に verb 限定の固定 systemd unit をもう1つ足すだけにし、任意コマンド注入を防ぐ polkit 境界を保った (却下: mode 引数で1 unit を分岐 — polkit の verb 限定性が緩む)。再接続は S6ab0ed のハンドシェイクを再利用 (priority_rule 6)。
- **アプリ install は「構造化データを正とし .nix を一方向生成」(§8.2) (S41bdf2)** — 手書き .nix の双方向パースはしない。`home.packages` ではなく `environment.systemPackages` を選択 (アプライアンスは OS レベルで home-manager を使わないため、AC の『相当の宣言』はこちら)。**コンテナへの到達は read-only `/nix/store` 共有** で実現 (Nix バイナリは patchelf 済みで自前 closure を RPATH に持つので Ubuntu コンテナでも動く)。これは Sd44947 の palmux-shared 金型に device を足す形で、新機構を作らず reconcile 冪等 (priority_rule 6)。install/共有どちらも全コンテナ一律 (金型=単一プロファイル維持)。
- **共有トグルは Sd44947 の shared_dirs と同一 source を指す (S41bdf2)** — カードの共有状態と汎用「共有フォルダ」リストが決して食い違わないように、別ストアを作らず既存の単一 source に書く (AC-2-1)。§8.3 の当初想定「共有=要 rebuild(重)」は Sd44947 で hot 化された実態に合わせ「即時反映(hot)」で表示し、陳腐化した設計ドキュメント (§7/§8.3/§8.4) も是正した。
- **Sprint タブは artifact を「読むだけ」に徹する (Se173ef, priority_rule 1)** — CLI/skill が真実、Palmux はミラー描画。タブは独自状態を持たず docs/sprint-logs をパースするのみ。再ドリフト防止に skill の SPRINT_LOGS_SCHEMA.json も実態へ更新 (リポジトリ外なので下記「確認」参照)。

## What to verify

- ⚠️ **(medium / release 依存) アプリ install の appliance.nix chown 変更は、次リリースで green に反映するまで手動準備なしでは効かない。** 現行 released イメージの green では `<flakeDir>/local` が palmux 所有でないため、リリース + `nixos-rebuild switch --flake .#appliance` を経るまで palmux2 が drop-in を書けない。green 実機 smoke ではこの dir 所有権だけ手で用意して load-bearing なロジックを実走させた (`compromises.json` blockers / `S41bdf2/green-smoke.json` に全手順)。→ 次リリース後、手動準備ゼロで install→rebuild→在コンテナ実行を再確認したい。
- ⚠️ **(S673a42 AC-2-4, 要ユーザ sign-off) 更新ボタンの「クリック→版数 delta→完了トースト」の実ブラウザ確認は release-gated で未実施。** green の palmux2 が既に最新 release (== latest) で、更新ボタンは `available` のときしか出ないため、production で bump する先が無い (S6ab0ed MS-1 / Sa8e7d0 と同じ既知の保留)。機構自体は green で endpoint→unit→`nix flake update`+`nixos-rebuild` rc=0 まで実走済み、delta トーストは mock でカバー。→ **installed より新しい release が出たタイミングで実機の完走確認をお願いしたい。**
- ⚠️ **(Se173ef Story 4, リポジトリ外) skill の `~/.claude/skills/sprint/references/SPRINT_LOGS_SCHEMA.json` は現行 artifact 全 9 種を既に反映済み** (編集不要と確認)。ただしこれはリポジトリ外なので batch の diff には出ない。将来 artifact が増えたらこのファイルも更新が要る点を認識しておいてほしい。
- **アプリカードの実挙動** (dev インスタンスで): インストールトグル ON→「要 rebuild(軽・即)」、共有トグル ON→「即時反映(hot)」、未導入で共有グレーアウト、nixpkgs 名の検証エラー表示。green では `incus exec <ctr> -- infisical --version → 0.41.1` を確認済みだが、UI の一連操作はあなたの目で。
- **新しい Sprint タブの見え方**: いまこのレポートが出ている trust-source-first の並び (機械判定/verifier/6-guard/compromises/comprehension/gui-spec mermaid) が読みやすいか。過去スプリントの実データで各サブタブ (Overview/Detail/Review/Milestones/Decisions/Dependencies/Backlog) を確認してほしい。

## What was assumed

- **`/nix/store` を read-only bind-mount すれば Ubuntu コンテナで Nix バイナリが動く**、という前提は green で二重に実証済み (host coreutils 9.7 と GUI 導入 infisical 0.41.1 が在コンテナで実行) だが、NSS/locale/CA-cert に強く依存する or closure 外の store パスへ shell out するツールでは今後エッジケースが出うる。カタログ拡張時は在コンテナ実行を smoke するのが安全。
- **アプリの認証フォルダ→パスの対応 (infisical→~/.infisical, gh→~/.config/gh) はキュレートしたカタログ規約**であり、Nix やアプリが self-declare するものではない。ユーザ定義アプリはパスを手入力する前提。
- **flakeDir は `/persist/palmux/nixos` 既定 + `PALMUX_NIXOS_FLAKE_DIR` で上書き可**と仮定。非アプライアンス配置で違うパスを使うなら env で渡す必要がある。
- **今回のバッチは同一作業ツリーで並行実行してしまい、途中で分離・回収した** (下記サマリ参照)。成果物は全て commit 済みで検証も通っているが、Se173ef は並行実行由来のバグ2件 (path traversal / CJK heuristic) を fixup で潰してから merge した経緯がある。
