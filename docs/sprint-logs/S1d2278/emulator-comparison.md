# S1d2278-1: Go 端末エミュレータ候補比較レポート

Sprint S1d2278 Track B PoC — PTY バイトストリームをサーバーサイドで消費してグリッドを保持するライブラリの選定評価。

---

## 1. 候補と理由

| # | ライブラリ | バージョン | 採用理由 |
|---|---|---|---|
| **A** | `github.com/charmbracelet/x/vt` | `v0.0.0-20260511125431-fe5d686e0c99` | charmbracelet エコシステム内で積極開発中の高機能 VT エミュレータ。`Emulator.Write()` で ANSI ストリームを直接消費でき、`CellAt(x,y)` でグリッドセルにアクセス可能。OSC/CSI ハンドラを差し替える拡張 API を持ち、コールバック経由でモード変化を監視できる。 |
| **B** | `github.com/hinshun/vt10x` | `v0.0.0-20220301184237-5011da428d02` | xterm/rxvt/st を参考にした vt10x 互換エミュレータ。`bufio.Reader` 経由の `Parse()` API と `Cell(x,y)` グリッド取得を持ち、シンプルな `ModeFlag` ビットフィールドでモード状態を公開している。go-ansiterm より実装が軽量。 |

> Candidate B の選定根拠: `github.com/Azure/go-ansiterm` は DEC Private Mode (`?1049h` などの alt screen) を実装しておらず、グリッド保持 API も持たないため除外した。`hinshun/vt10x` は軽量で実用的な `View` インターフェースと `ModeFlag` を備える。

---

## 2. 6 軸カバレッジ表

| 機能 | A: charmbracelet/x/vt | B: hinshun/vt10x |
|---|---|---|
| **alt screen** (`?1047h`/`?1049h`) | ✓ `IsAltScreen()` + `AltScreen` コールバックで検出。入退両方トラッキング。 | ✓ `ModeAltScreen` フラグで検出。`?1049h` も処理される。 |
| **OSC 52 クリップボード** | ✓ `RegisterOscHandler(52, fn)` でペイロードをコールバック受信（実測: `"52;c;aGVsbG8="` を返す）。 | ✗ OSC シーケンス用コールバック API なし。OSC 52 はサイレント無視。自前パーサが必要。 |
| **bracketed paste** (`?2004h`) | ✓ `EnableMode` コールバックに `ansi.ModeBracketedPaste` が渡される。 | partial `ModeFlag` に `?2004h` 対応ビットなし。シーケンスは parse されるが外部から照会不可。 |
| **mouse SGR** (`?1006h`) | ✓ `EnableMode` コールバックに `ansi.ModeMouseExtSgr` が渡される。 | ✓ `ModeMouseSgr` フラグ（実測で set 確認）。 |
| **sixel / グラフィクス拡張** | ✗ sixel 非対応。DCS シーケンスは `RegisterDcsHandler` で自前フックは可能だが描画なし。 | ✗ sixel 非対応。 |
| **スクロールバック** | ✓ `Scrollback()` + `ScrollbackCellAt()` で最大 10,000 行デフォルト。`SetScrollbackSize()` で変更可。 | partial `State` が primary/alt の 2 スクリーンを持つが、公開 scrollback API はなし。 |

---

## 3. メンテ状況

| 項目 | A: charmbracelet/x/vt | B: hinshun/vt10x |
|---|---|---|
| 最終コミット | 2026-04-30 | 2022-03-01 |
| 直近 3 ヶ月のコミット数 | 10+ (2026-02〜05 の活発な開発) | 0 (4 年間更新なし) |
| Go モジュールバージョン形式 | pseudo-version (プレリリース) | pseudo-version (安定 API だが最終 tag なし) |
| Open Issues | モノレポ全体: 多数あるが vt 専用ラベルでのヒットは少ない | 1 件 (放置気味) |
| メンテナ | charmbracelet チーム (複数人) | hinshun 個人 (非アクティブ) |

---

## 4. 自前実装で書く場合の工数推定

palmux2 Track B に必要な最小サブセット（alt screen / cursor / SGR 色 / CSI カーソル移動 / OSC 52）だけを自前実装する場合:

| スコープ | 推定 LoC |
|---|---|
| 最小サブセット（alt screen + 基本 cursor + SGR 16 色 + OSC 52） | 600〜900 LoC |
| 256 色 + true color + bracketed paste + mouse | +400〜600 LoC |
| スクロールバック + 完全 CSI サポート | +600〜1000 LoC |
| プロダクショングレード（xterm 準拠レベル） | 2,000+ LoC |

いずれも既存ライブラリの `interface` に差し込める形に設計すれば、将来の差し替えも容易。ただし ANSI パーサ自体の実装は `github.com/charmbracelet/x/ansi` を流用すれば 300〜500 LoC 削減できる。

---

## 5. 推奨

**推奨: Candidate A (`github.com/charmbracelet/x/vt`) をベースに採用**

### 理由

1. **OSC 52 コールバック対応** — claudeTab の最重要ユースケース（クリップボード連携）が追加パーサなしで動く。vt10x は OSC 52 を完全無視するため追加実装が必要。
2. **積極メンテ** — 2026 年 4 月時点でも活発に更新されており、将来の ANSI 拡張（XTGETTCAP 等）も期待できる。vt10x は 2022 年以降放置。
3. **モード状態の可観測性** — `EnableMode` / `DisableMode` コールバックにより、bracketed paste・mouse・alt screen すべてのモード遷移をリアクティブに監視できる。これはサーバーサイドレンダリングで重要。
4. **スクロールバック API** — claudeTab では長い LLM 出力を保持する必要があり、`Scrollback()` が使える Candidate A が有利。
5. **コールバック拡張性** — `RegisterCsiHandler` / `RegisterOscHandler` で未知シーケンスを自前実装に差し替えられる設計が palmux2 の拡張ニーズと合致する。

### 留意事項

- charmbracelet/x/vt はまだ pseudo-version（プレリリース）であり API 安定性は保証されていない。Track B 本実装時は特定コミット hash を pin する。
- OSC 52 ペイロードの形式が `"52;c;<b64>"` と prefix 付きで渡るため、ハンドラ内で `;` 分割が必要（ドキュメント未整備）。
- sixel は両ライブラリとも非対応。Claude TUI が sixel を使う場合は追加対応が必要だが、現時点の CLI 版 claude は sixel を送出しない。

**結論: Track B は Candidate A (`charmbracelet/x/vt`) で技術的に実現可能。**
