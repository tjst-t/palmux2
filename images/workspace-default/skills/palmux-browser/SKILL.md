---
name: palmux-browser
description: Control the palmux shared browser tab (chromium via CDP). Use this to start, navigate, interact with, and screenshot a browser that the user also sees live. No MCP — runs via the `palmux-browser` CLI which is already in PATH inside the container.
---

# palmux-browser skill

## What this is

`palmux-browser` is a CLI that connects to the **same chromium instance** the user sees in the **Browser tab** of the palmux UI. You and the user share one browser: the user can see everything you navigate to, and you can see what the user has loaded.

There is no separate browser process — `palmux-browser` connects to the running chromium over CDP (`connectOverCDP`). No new browser download is needed.

## Lifecycle

```
1. Check if running:
   $ palmux-browser status
   # prints: running | stopped | starting

2. Start if stopped:
   $ palmux-browser start
   # starts chromium, posts an Activity Inbox notification so the user knows

3. Operate:
   $ palmux-browser navigate https://example.com
   # url: https://example.com/
   # title: Example Domain

   $ palmux-browser snapshot
   # lists ≤40 interactable elements (role | name | selector)

   $ palmux-browser click button#submit
   $ palmux-browser type input[name="q"] "search term"

   $ palmux-browser screenshot
   # /tmp/palmux-screenshot-1234567890.png

4. Stop when done (optional — the user may want it left open):
   $ palmux-browser stop
```

## Key facts

- **Shared session**: cookie/session/profile are shared with the user's Browser tab. One login = both can see authenticated pages.
- **CDP endpoint**: derived from `PALMUX_CDP_URL` (if set) or the container bridge IP (`hostname -i`). Already configured when palmux spawns claude.
- **REST lifecycle**: `start`/`stop`/`status` call the palmux REST API so the Browser tab UI stays consistent.
- **Activity Inbox**: `palmux-browser start` posts a notification so the user sees "Browser started by Claude" in their inbox.
- **Output is terse**: only the essential result is printed (URL + title, path, or element list). No large a11y dumps.

## Subcommands

| Command | What it does |
|---|---|
| `status` | print `running` / `stopped` / `starting` |
| `start` | start chromium + notify inbox |
| `stop` | stop chromium |
| `navigate <url>` | go to URL, print url + title |
| `click <selector>` | click CSS selector |
| `type <selector> <text>` | fill element with text |
| `snapshot` | compact list of ≤40 interactable elements |
| `screenshot [path]` | save PNG, print path only |

## Error handling

If chromium is not running and you try `navigate`/`click`/`type`/`snapshot`/`screenshot`, you will get an error. Always check `status` first and `start` if needed.
