# herdr-notifications

A [herdr](https://herdr.dev) plugin that sends a Telegram message when an agent in a pane finishes (`done`) or needs input (`blocked`). The message includes the last lines of the pane's output, so you can often tell from your phone whether it needs you.

Example message:

```
✅ claude finished
📁 my-project › api
Fix flaky auth test

$ go test ./...
ok  	example.com/api/auth	0.412s
ok  	example.com/api/store	1.037s
```

Line 1 is `✅ <agent> finished` or `⏸ <agent> needs input`. Line 2 shows the workspace and tab that contain the pane. Line 3 is the pane title, shown only when it differs from the agent name. The pane tail comes last. It is sent as a `<pre>` block with ANSI codes stripped, and its oldest lines are cut if the message would go over Telegram's 4096-character limit.

## Requirements

- herdr 0.9.1 or newer (Linux or macOS)
- Go 1.23 or newer, used to build the binary. There are no dependencies outside the standard library.

## Install

From GitHub:

```sh
herdr plugin install wurthel/herdr-notifications
```

herdr runs the manifest's `[[build]]` step (`go build ...`) and puts the `herdr-notifications` binary in the plugin root.

For local development:

```sh
git clone https://github.com/wurthel/herdr-notifications
cd herdr-notifications
make link      # builds the binary, then runs `herdr plugin link .`
```

`herdr plugin link` does **not** run `[[build]]`. `make link` builds first. If you call `herdr plugin link .` yourself, run `make build` before it and again after every code change.

To remove a linked plugin, run `make unlink`.

## Configuration

1. In Telegram, open a chat with [@BotFather](https://t.me/BotFather), send `/newbot` and copy the token.
2. Send your new bot any message. Then open `https://api.telegram.org/bot<TOKEN>/getUpdates` and copy `result[].message.chat.id`.
3. Put a `.env` file in the plugin's config directory:

   ```sh
   dir="$(herdr plugin config-dir herdr-notifications)"
   mkdir -p "$dir"
   cp .env.example "$dir/.env"   # then edit it
   chmod 600 "$dir/.env"
   ```

4. Send a test message: `herdr plugin action invoke test --plugin herdr-notifications`.

| Key | Default | Meaning |
| --- | --- | --- |
| `TELEGRAM_BOT_TOKEN` | (required) | Bot token from @BotFather |
| `TELEGRAM_CHAT_ID` | (required) | Chat to send messages to |
| `NOTIFY_ON` | `done,blocked` | Comma-separated statuses that trigger a message: `idle`, `working`, `blocked`, `done` |
| `PANE_TAIL_LINES` | `15` | Number of pane output lines to include (max `200`). `0` disables the tail. |
| `NOTIFY_IDLE_AFTER_WORKING` | `1` | Treat `working → idle` as `done` (the agent finished in a visible pane). Needs `done` in `NOTIFY_ON`. `0` disables. |
| `DEBOUNCE_SECONDS` | `10` | Minimum seconds between two messages for the same status on the same pane |
| `DEBUG` | `0` | `1`/`true` writes the latest event to `$HERDR_PLUGIN_STATE_DIR/debug/last-event.json` |
| `TELEGRAM_API_BASE` | `https://api.telegram.org` | Bot API base URL, for tests or a self-hosted Bot API server |

A variable set in the process environment overrides the same key in `.env`, unless it is empty. Hooks run inside the herdr server, so this means the server's environment from when herdr started. Exporting a variable in your own shell later has no effect, so prefer `.env`. The `.env` format accepts `KEY=value` lines, `#` comments (including a trailing ` # comment` on unquoted values), an optional `export ` prefix and quoted values. Unknown `NOTIFY_ON` statuses and out-of-range numbers are reported as warnings in the plugin log.

Never commit your token. `.env` is already in `.gitignore`.

## How status detection works

herdr gives each pane one of these agent statuses: `idle`, `working`, `blocked`, `done` or `unknown`. On every `pane.agent_status_changed` event, the plugin runs `./herdr-notifications notify` and applies these rules:

- `done` means the agent went idle and you have **not seen it yet**. Agents in a background tab report `done`. If the pane is visible in an attached herdr client when the agent finishes (for example a split next to the one you are typing in), herdr treats it as seen and reports `idle` instead. Since you may have walked away with that split on screen, a direct `working → idle` transition is also reported as "finished" (`NOTIFY_IDLE_AFTER_WORKING=1`, the default). Merely looking at an already finished pane (`done → idle`) never sends anything.
- A status only counts when it changes. herdr may send the same status more than once, and the plugin records each pane's last status so repeats are ignored.
- Debounce works per status. For each pane, a status in `NOTIFY_ON` triggers at most one message every `DEBOUNCE_SECONDS`.
- `unknown` is ignored and not recorded, so `done → unknown → done` counts as no change and never sends a second message.
- If sending fails (network down, Telegram error), the notification is rolled back and the next event with the same status retries it.
- Pane state is stored in `$HERDR_PLUGIN_STATE_DIR/panes/`, one small JSON file per pane, guarded by a `flock`. Files are not pruned; delete the directory at any time to reset.
- herdr's payload has no sequence number, so two events for one pane that fire at the same instant are processed in lock order, not necessarily event order. Debounce hides almost all of these cases.

## Actions

```sh
herdr plugin action invoke test   --plugin herdr-notifications   # send a test message
herdr plugin action invoke toggle --plugin herdr-notifications   # mute / unmute
```

`toggle` creates or removes a `disabled` file in the state directory. It prints the new state and asks herdr to show a notification about it. Whether that notification appears depends on your `[ui.toast]` config. While muted, events still arrive but no messages are sent.

To bind a key to the toggle action, add this to `~/.config/herdr/config.toml`:

```toml
[[keys.command]]
key = "prefix+shift+t"
type = "plugin_action"
command = "herdr-notifications.toggle"
```

## Troubleshooting

- **No message arrives.** Run `herdr plugin log list --plugin herdr-notifications`. The event hook always exits 0 and writes errors (missing token, Telegram API errors, pane read failures) to stderr, which herdr saves in this log.
- **Is the plugin loaded?** Run `herdr plugin list`.
- **"No such file" or exec errors.** Hooks run inside the herdr server, which runs `./herdr-notifications` from the plugin root. That binary must exist there. For a linked checkout, run `make build`.
- **Unsure what herdr sends.** Set `DEBUG=1` and trigger an event, then read the file it overwrites on each event, `$HERDR_PLUGIN_STATE_DIR/debug/last-event.json`.
- **Nothing is sent after the agent finishes.** Check whether `NOTIFY_IDLE_AFTER_WORKING` is disabled while the pane was visible (see "How status detection works"), whether notifications are muted (`test` reports this), whether `NOTIFY_ON` includes the status and whether debounce dropped it. For example, a quick `done → working → done` on one pane sends only one message within `DEBOUNCE_SECONDS`.

## Development

```sh
make build   # go build -trimpath -ldflags="-s -w"
make test    # go test -race ./...
make lint    # fails if gofmt reports changes, then go vet
make fmt     # gofmt -w .
make clean
```

## Project layout

```
.
├── herdr-plugin.toml      # manifest: build step, event hook, actions
├── main.go                # CLI entry point: notify | test | toggle
├── internal/
│   ├── config/            # .env + environment loading
│   ├── event/             # parses HERDR_PLUGIN_EVENT_JSON / CONTEXT_JSON
│   ├── herdr/             # herdr CLI calls (pane read, notification show)
│   ├── notify/            # notification decision + message formatting
│   ├── state/             # per-pane state, mute flag, file locking
│   └── telegram/          # Bot API client
├── .env.example
└── Makefile
```
