# Sprint S006 — Autonomous Decisions

> Sprint: S006 — `--add-dir` / `--file` UI
> Branch: `autopilot/S006`
> Started: 2026-04-30

## Summary

Composer の `+` メニューから「Add directory」「Add file」を選び、ワークツリー内の任意のパスを **送信時のコンテキストに付ける** UI を実装する。

## CLI 検証結果 (claude 2.1.123)

`claude --help` で実際のフラグ形状を確認した。これが本スプリント全体の前提になる:

| フラグ | 形状 | 用途 |
|---|---|---|
| `--add-dir <directories...>` | repeatable, space-separated | ツールアクセス許可ディレクトリの追加 (ローカルパス OK) |
| `--file <specs...>` | `file_id:relative_path` 形式 | **Anthropic File API の resource を起動時にダウンロード** する用途。ローカルファイルパスではない |

つまりロードマップ S006-1 が想定する「`--file <path>` でローカルファイルを渡す」は、CLI 2.1.123 では **直接そうは書けない**。ロードマップのユーザーストーリー（"現在の worktree に含まれていないコードや仕様書を Claude に参照させたい"）の意図は **「ファイルの中身を Claude のコンテキストに入れたい」** である。CLI の現実的なメカニズムは:

- ディレクトリ追加: `--add-dir <abspath>` (再起動が必要)
- ファイル追加: `@<abspath>` を user message 本文にインライン注入 (画像添付と同じパターン、再起動不要)

`@<path>` は Composer が既に `@` autocomplete でサポートしている Claude Code idiom — assistant が `Read` ツールでそのパスを読みに行く動きに変わる。

### 決定 D-1: `--file` は使わない、`@<abspath>` インラインで実装する

- **根拠**: CLI が真実 (DESIGN_PRINCIPLES 1)。`--file <file_id:relpath>` に偽の file_id を入れても CLI が Anthropic API に投げて 4xx になるだけで、ユーザの意図 (ローカルファイル参照) を満たさない
- **副作用**: ロードマップ「`--file <path>` として CLI に渡る」の文言とズレるが、**ユーザーストーリー (worktree 外のファイルを参照させたい)** は完全に満たせる。実装後にロードマップ受け入れ条件を「ローカルファイルは送信時のメッセージに `@<abspath>` として展開」に修正する
- **明示性**: 添付チップは「📄 file」表示のままで、ユーザは何が CLI に送られるか追える (DESIGN_PRINCIPLES 4 「明示的 > 暗黙的」)

### 決定 D-2: ディレクトリ追加は `--add-dir` で respawn

- 既存の `IncludeHookEvents` 経路 (S005) と同じパターン。CLI startup-only flag なので mid-session で in-band に追加できない
- 再起動は `respawnClient()` 既存ヘルパー利用、`--resume <session_id>` で会話を継続
- 添付チップの追加・削除は **送信時にスナップショット** を取って argv に詰める。送信ごとに argv が変わる場合は respawn を発火する
- **永続化スコープ**: per-message (送信のたびにフレッシュ) — per-branch defaults は roadmap 範囲外と明記

## ピッカースコープ決定

### 決定 D-3: ピッカーはワークツリー内のみ (host filesystem は S006 範囲外)

- **根拠**:
  - VISION: 「シングルユーザ・自前ホスティング」前提なので host 公開そのものはセキュリティ的に致命的ではない
  - DESIGN_PRINCIPLES 2 「責務越境最小 > 便利さ」: 偶発的なホスト走査エンドポイントを生やさず、明示的なオプトインが必要
  - 既存 Files tab の `SearchEntries` / `ListDir` を流用すれば再実装不要 (DESIGN_PRINCIPLES 3 「既存資産活用」)
  - host picker を実装するなら別 affordance + 別エンドポイント + 別 auth スコープにすべきで、それは backlog に切り出す
- **実装**: ワークツリー root を起点に、Files tab の `resolveSafePath` (traversal + symlink 検証) を流用
- **API**:
  - 既存 `GET /api/repos/{repoId}/branches/{branchId}/files/search?query=...` (ファイルとディレクトリの両方を返す) を再利用
  - `--add-dir` には worktree-relative path を **絶対パスに正規化して** 渡す (`worktree + "/" + relpath`)。CLI は cwd-relative も受けるが、worktree が CLI の `--cwd` (= `cmd.Dir`) と一致するか保証されない場合に備えて絶対化する
- **チップ表示**: 「📁 path/」(ディレクトリ) / 「📄 path」(ファイル) の worktree-相対表示 — ユーザは「ワークツリー内のもの」と認識できる

### 決定 D-4: ピッカー UI は新規ピッカーポップアップを Composer の `+` メニューから開く

- 「+」ボタンを Composer に追加 (現状ない、画像は paste/drag-drop のみ受けている)
- メニューに「Add directory」「Add file」「Upload image…」の 3 アクション (image 既存路は menu 経由でも残す)
- ピッカーは inline-completion popup と同じ Files API を叩く simple search ダイアログ。タイプして検索、Enter / クリックで添付
- モバイル parity: 同じポップアップで動く。`<input type="file">` を使わない (worktree 検索なので native file picker は ホスト全体 を見せてしまう、これも責務越境)

## 実装決定

### 決定 D-5: Attachment 型を `kind: 'image' | 'dir' | 'file'` に拡張

- 既存の `image | file` を完全に置き換え (`file` は image upload 系の "添付ファイル" として一度も使われていなかったので、意味を「Add file picker で選んだローカルファイル」に再利用する)
- 新フィールド: `relPath` (ワークツリー相対表示用) — `path` (絶対パス) は既存
- 画像との見分け: `kind === 'image'` のみサムネイル、それ以外は icon + text

### 決定 D-6: 送信時の振り分け

```
attachments.filter(kind === 'dir').map(a => a.path)   // → addDirs (CLI argv)
attachments.filter(kind === 'file').map(a => `@${a.path}`)  // → message 本文末尾に注入
attachments.filter(kind === 'image').map(a => `[image: ${a.path}]`)  // 既存
```

- `dir` 添付は `user.message` フレームの新フィールド `addDirs: string[]` で渡す
- BE 側: `SendUserMessage(content, addDirs)` に `addDirs` を渡し、 `agent.addDirs` (live state) と差分比較して必要なら respawn → CLI 起動 → message 送信

### 決定 D-7: respawn の発火条件

- 「送信ごと」ではなく **「addDirs の集合が変わったとき」** のみ。同じ dirs が連続で渡されたら no-op
- `agent.addDirs` (set/[]string) を持ち、`SendUserMessage(content, addDirs)` で:
  1. 新 addDirs の集合 ⊆ 旧集合 → no respawn (CLI は既に許可済み)
  2. 新 addDirs に未登録のものがある → 旧集合 ∪ 新集合 を保存し respawn
  3. ユーザが減らしたいときは別経路 (将来 backlog) — まずは「increase only」で OK
- これにより「3 通連続でディレクトリ添付して送る」と最初の 1 回だけ respawn (ユーザが感じる遅延は減る)

## E2E テスト計画

`tests/e2e/s006_add_dir_file.py`:

1. **REST API 防御テスト** (no browser):
   - `GET /api/repos/{repo}/branches/{branch}/files/search?query=../etc/passwd` → 結果空 or 200 だが path が `..` 経由でない (search は base path を `resolveSafePath` で検証してから走るので、 traversal は弾かれる)
   - `?path=../../etc&query=passwd` → 400 (`ErrInvalidPath`)
2. **Composer の `+` メニュー UI** (browser):
   - `+` ボタンが見える
   - クリックで「Add directory」「Add file」メニュー項目が出る
3. **Picker 動作**:
   - 「Add directory」クリック → ピッカーが出る
   - 検索フィールドに "internal" と入力 → 結果に `internal` ディレクトリが出る
   - クリック → 添付チップ「📁 internal」が表示
4. **チップ削除**:
   - チップの × ボタン → チップ消える
   - WS frame ペイロードに addDirs が空配列で含まれる
5. **送信フロー (synthetic、CLI なし)**:
   - フェイク添付 (dir + file) → 送信
   - WS framesent ペイロードに `{type:'user.message', payload:{content:'... @<abspath>', addDirs:[<abspath>]}}` が含まれる
6. **CLI argv 観察**:
   - dev インスタンスのログから `claude` 起動時の argv に `--add-dir <abspath>` が現れることを確認 (ps -ef で claude プロセスを拾うか、palmux ログを grep)

## 不確実性

- 添付チップの永続化: 現状の image attachment は UI state のみ (リロードで消える)。dir/file も同じ揮発挙動でよい (per-message の意図に合致) — backlog に「per-branch persisted attachments」を切り出す
- モバイル UX: ピッカーは Composer 上に重ねるシンプルなダイアログ → 既存 InlineCompletionPopup と同じ縦並びリスト

## 検証結果

### Go ユニット試験 (`internal/tab/claudeagent/add_dirs_test.go`)

8 ケース PASS:

- `TestValidateAddDirs_AcceptsRelativeInsideWorktree` — worktree 内の相対パス OK
- `TestValidateAddDirs_RejectsParentTraversal` — `../etc/passwd`, `foo/../../etc`, `..` の 3 形態を拒否
- `TestValidateAddDirs_RejectsAbsoluteOutsideWorktree` — 別 TempDir を渡して "outside worktree" エラー
- `TestValidateAddDirs_AcceptsAbsoluteInsideWorktree` — worktree 内の絶対パス OK
- `TestValidateAddDirs_DedupesDuplicates` — 同じパスを 3 回渡して 1 件に縮約
- `TestValidateAddDirs_RejectsSymlinkEscape` — worktree 内 symlink → 外部ディレクトリのケースも拒否
- `TestValidateAddDirs_DropsEmpty` — 空文字列は無視
- `TestMergeAddDirs_GrowsAndDedupes` — 既存集合との和集合と respawn 判定

```
$ go test ./internal/tab/claudeagent/ -run 'TestValidateAddDirs|TestMergeAddDirs' -v
... 8/8 PASS
```

### Playwright E2E (`tests/e2e/s006_add_dir_file.py`)

dev インスタンス (port 8245) に対して 14 チェックを実行、全 PASS:

```
PASS: REST search rejects path=../../etc with 400
PASS: REST listDir rejects path=../../etc with 400
PASS: REST search returns 14 results for 'internal', none containing '..'
PASS: page loaded; composer textarea present
PASS: composer + button visible
PASS: attach menu shows Add directory / Add file / Upload image
PASS: path picker opened for dir kind
PASS: dir picker results all directories (4 items)
PASS: dir chip attached: path=frontend/node_modules/.../semver/internal
PASS: dir chip removable via × button
PASS: file chip attached: path=go.mod
PASS: user.message frame carries addDirs and inline @-mention as designed
PASS: second user.message (no chips) omits addDirs field
PASS: real claude process observed with --add-dir in argv
```

最後の "real claude process observed" は **実機確認**: 送信後の `ps -eo pid,cmd` 出力で

```
claude --input-format stream-json --output-format stream-json --include-partial-messages
       --verbose --setting-sources project,user
       --permission-prompt-tool mcp__palmux__permission_prompt
       --permission-mode auto --effort xhigh
       --add-dir /home/ubuntu/ghq/github.com/tjst-t/palmux2/frontend/node_modules/@typescript-eslint/typescript-estree/node_modules/semver/internal
```

を確認。`--add-dir <abspath>` が実 CLI のコマンドラインに **絶対パスで** 入っており、`validateAddDirs` がワークツリー内 → 絶対パス変換 → argv まで通っている。

### 副次バグ・観察

- **Enter キーの submit が Playwright で発火しない件**: 当初 `textarea.press("Enter")` で submit していたが user.message フレームが飛ばなかった。原因は完全には特定していないが、 inline-completion handler の Enter 介入か Playwright の textarea focus 経路と思われる。回避策として `button[aria-label=Send]` を click する形に変更し、これで安定。この件は別 backlog (E2E 安定化) に切り出さず、テスト側の選択肢で済む
