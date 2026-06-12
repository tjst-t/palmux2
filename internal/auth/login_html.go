package auth

// loginHTML is the self-contained server-rendered login page (Sbe4eee). It is
// NOT the React SPA — the SPA is itself behind forward_auth, so the login page
// must be reachable un-authenticated. Mirrors prototype/sbe4eee-login.html.
// Template fields: .RD (return URL, html/template auto-escapes), .Error, .Domain.
const loginHTML = `<!doctype html>
<html lang="ja" data-theme="dark">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>PALMUX — ログイン</title>
<style>
:root{--bg:#0f1117;--surface:#13151c;--border:#1e2028;--fg:#d4d4d8;--fg-muted:#8b8fa0;--fg-faint:#4a4e5c;--accent:#7c8aff;--accent-light:#9ba6ff;--error:#ef4444;--ui:"Geist","Noto Sans JP",-apple-system,BlinkMacSystemFont,sans-serif;--mono:"Geist Mono","Cascadia Code","Fira Code",monospace}
*{box-sizing:border-box}
body{margin:0;font-family:var(--ui);background:radial-gradient(1200px 600px at 50% -10%,rgba(124,138,255,.10),transparent 60%),var(--bg);color:var(--fg)}
.wrap{min-height:100vh;display:flex;align-items:center;justify-content:center;padding:24px}
.card{width:100%;max-width:360px;background:var(--surface);border:1px solid var(--border);border-radius:14px;padding:32px 28px 26px;box-shadow:0 20px 60px rgba(0,0,0,.45)}
.brand{display:flex;align-items:center;gap:10px;justify-content:center;font-family:var(--mono);font-weight:700;letter-spacing:.14em;font-size:15px}
.brand .dot{width:9px;height:9px;border-radius:50%;background:var(--accent);box-shadow:0 0 12px var(--accent)}
.sub{margin:10px 0 22px;text-align:center;font-size:12.5px;color:var(--fg-muted);line-height:1.6}
.sub code{font-family:var(--mono);color:var(--fg);font-size:11.5px}
.err{margin-bottom:16px;padding:9px 12px;border-radius:8px;background:rgba(239,68,68,.08);border:1px solid rgba(239,68,68,.32);color:var(--error);font-size:12.5px;line-height:1.5}
.field{margin-bottom:14px}
.label{display:block;font-size:11px;color:var(--fg-muted);margin-bottom:6px;letter-spacing:.02em}
.input{width:100%;background:var(--bg);border:1px solid var(--border);border-radius:8px;padding:11px 12px;color:var(--fg);font-size:14px;font-family:var(--ui);outline:none}
.input:focus{border-color:var(--accent);box-shadow:0 0 0 3px rgba(124,138,255,.18)}
.remember{display:flex;align-items:center;gap:8px;margin:4px 0 20px;cursor:pointer;user-select:none}
.remember input{width:15px;height:15px;accent-color:var(--accent)}
.remember span{font-size:12.5px;color:var(--fg-muted)}
.submit{width:100%;padding:11px;border:none;border-radius:8px;background:var(--accent);color:#0c0e14;font-weight:600;font-size:14px;font-family:var(--ui);cursor:pointer}
.submit:hover{background:var(--accent-light)}
.foot{margin-top:18px;text-align:center;font-size:10.5px;color:var(--fg-faint)}
</style>
</head>
<body>
<div class="wrap">
<form class="card" method="POST" action="/auth/login" data-testid="auth-login-form">
<input type="hidden" name="rd" value="{{.RD}}">
<div class="brand" data-testid="auth-brand"><span class="dot"></span> PALMUX</div>
<div class="sub"><code>{{.Domain}}</code> とその全サブドメインに<br>1回のログインでアクセスできます。</div>
{{if .Error}}<div class="err" data-testid="auth-error">{{.Error}}</div>{{end}}
<div class="field">
<label class="label" for="pw">パスワード</label>
<input class="input" id="pw" name="password" type="password" autocomplete="current-password" autofocus placeholder="••••••••" data-testid="auth-password-input">
</div>
<label class="remember" data-testid="auth-remember-label">
<input type="checkbox" name="remember" checked data-testid="auth-remember-checkbox">
<span>このブラウザを記憶する（次回から再入力なし）</span>
</label>
<button class="submit" type="submit" data-testid="auth-submit">ログイン</button>
<div class="foot">🔒 単一ユーザ・自前ホスティング — Cookie はこのドメインのみ</div>
</form>
</div>
</body>
</html>`
