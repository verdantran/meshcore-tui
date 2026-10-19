# Configuration

Everything has a default, so the config file is optional. The app writes to it itself when you save a device, change the split-pane layout or set something from a screen. It rewrites the whole file when it does, so comments you add there don't survive.

## Where things live

| | macOS and Linux | Windows |
|---|---|---|
| Config (`config.toml`, `keys.toml`) | `~/.config/meshcore-tui/` | `%APPDATA%\meshcore-tui\` |
| Data (message history, contacts, watched nodes' status, map detail, exports) | `~/.local/share/meshcore-tui/` | `%LOCALAPPDATA%\meshcore-tui\` |

`XDG_CONFIG_HOME` and `XDG_DATA_HOME` override these on every platform, and `--config` points at a different config file.

Repeater and room-server passwords are kept in the OS keychain (Keychain on macOS, Secret Service on Linux, Credential Manager on Windows), never in the config. Where there's no keychain, as on a headless Linux server, they go in `passwords.json` beside the config, readable only by you.

## config.toml

Every setting, with its default:

```toml
[connection]
auto_connect = ""          # the name of a saved device to connect to at startup

[messages]
dm_retries = 3             # attempts at a direct message before it's marked failed
flood_fallback = true      # make the last attempt by flood, forgetting the stored route
sign_channels = []         # channels whose messages you send are signed, e.g. ["#announcements"]
keep_days = 0              # delete messages received more than this many days ago; 0 keeps every message

[notifications]
events = ["dm", "mention"] # what notifies: direct messages, and mentions of you in channels
bell = true                # ring the terminal bell
desktop = "auto"           # auto | terminal | os | bell | off
show = "message"           # how much reaches the desktop and lock screen: message | sender | nothing
max_per_minute = 6         # the most notifications in any minute; more are gathered into one summary; 0 is no limit

[admin]
status_poll_s = 60         # how often a node's status refreshes once auto-refresh is on
fleet_poll_spacing_s = 20  # the gap between nodes when the fleet view polls them
battery_alert_v = 3.5      # alert below this, as a Li-ion voltage; other batteries alert at the same charge
stale_after_h = 24         # flag a node in the fleet view when it hasn't advertised for this long
watch = []                 # public keys of nodes to poll and keep history for; w on Admin toggles
watch_poll_m = 10          # minutes between polls of a watched node over the air
watch_local_s = 60         # seconds between polls of a node on USB

[batteries]                # battery type by public key, for reading charge; li-ion when not listed
# "A3CE…" = "lifepo4"      # li-ion | lipo | lifepo4 | none (wired, never alerts)

[locations]                # nodes placed by hand (l on Contacts), by public key
# "5F12…" = [51.41, -2.68]

[ui]
theme = "neon"             # neon | dusk | vapor | ice | midnight | ember | amber | toxic | phosphor | light | paper | high-contrast | mono
motion = ""                # full | reduced | off; empty is full, or reduced over SSH
packet_ticker = true       # the scrolling hex of each packet heard, under the event log
beacon = true              # the rings pulsing around you on the map, at full motion
mouse = true               # clicks pick screens and panes; off leaves the mouse to the terminal, for selecting text
layout = ""                # split panes, e.g. "right 50 (*chats, packets)"; * marks the focused one
ascii = false              # plain ASCII for terminals without braille or box drawing
```

Over SSH or mosh, motion starts at `reduced`; inside tmux the app asks tmux how you're attached, so a tmux started on the server and attached over SSH counts too. If frames tear inside tmux and your terminal supports synchronized output (iTerm2, kitty, WezTerm, Ghostty, foot, Windows Terminal), tell tmux so with `set -as terminal-features ',xterm*:sync'` (matching your `TERM`), then reattach.

Saved devices are kept as `[[devices]]` entries. They're easiest to manage from the connect screen.

### Message history

`keep_days` is how many days of messages to keep, on every radio. Older ones are deleted when the app connects and once a day while it runs; messages you sent that are still waiting to be delivered never are. `0`, the default, keeps everything. `:history keep 365` or `:history keep all` changes it from inside the app, and asks first if it would delete anything, saying how many.

### Desktop notifications

`auto` picks the best method for where you are:

- **terminal**: an escape sequence the terminal turns into a notification. Ghostty, iTerm2, WezTerm and kitty support it, and it's the only kind that reaches your desktop over SSH.
- **os**: `osascript` on macOS, `notify-send` on Linux, a toast through PowerShell on Windows.
- **bell**: the terminal bell only.
- **off**: nothing beyond the in-app toast, which always shows.

`max_per_minute` keeps a busy mesh from ringing all night: past the limit, the toast still shows in the app, and the rest arrive as one "N more messages" once the minute has room.

A muted conversation still keeps its messages and unread count, but never notifies, mentions included. `m` on Chats mutes or unmutes the open one; `:mute 8h` mutes it for a while, and `:mute #bristol` or `:unmute Bob` names one. Mutes are saved in the config:

```toml
[[notifications.muted]]
channel = "#bristol"
until = 2026-10-20T08:00:00Z   # leave out to mute until unmuted

[[notifications.muted]]
contact = "3AAA01…"            # the contact's public key
```

`show` decides how much of a message leaves the app, since notifications can appear on a locked screen: `message` shows who it's from and the text, `sender` only who it's from, `nothing` only that there's a new message. The in-app toast always shows the whole message. `:notify show sender` changes it as you go.

## Keys

`keys.toml`, beside the config file, rebinds keys. Each line makes a key do what another key does by default. Put it under `[all]`, under one screen (`connect`, `chats`, `contacts`, `map`, `packets`, `admin`, `device` or `stats`), or under a view within a screen:

| Section | When it applies |
|---|---|
| `[chats.reading]` | A message is picked in Chats |
| `[contacts.heard]` | The list of nodes heard but not added |
| `[map.path]` | The map is showing one path |
| `[admin.list]` | Admin's node list |
| `[admin.status]`, `[admin.health]`, `[admin.settings]`, `[admin.neighbours]`, `[admin.access]`, `[admin.regions]`, `[admin.telemetry]`, `[admin.log]` | That pane of an open node |
| `[panes]` | The key after `ctrl+w` |

The most particular section wins, then the screen's, then `[all]`. Binding a key to `""` makes it do nothing there.

```toml
[all]
"ctrl+n" = "down"
"ctrl+p" = "up"

[map]
w = "up"
a = "left"
s = "down"
"ctrl+d" = "d"  # street detail moves to ctrl+d
d = "right"
f = ""          # no picking out a node's traffic

[admin.settings]
z = "x"         # z discards an edit here, and only here

[panes]
b = "s"         # ctrl+w b splits down
```

Keys are named as Bubble Tea names them: `w`, `ctrl+n`, `alt+left`, `f5`. A text field takes keys as you type them, and `ctrl+c` always quits. Each screen's help (`?`) lists the rebindings in force.

## Command-line flags

Flags override the config file for that run.

| Flag | |
|---|---|
| `--serial PATH` | Connect to a USB serial port |
| `--ble ADDRESS` | Connect over Bluetooth |
| `--tcp HOST[:PORT]` | Connect to a WiFi companion (port 5000 by default) |
| `--repeater` | With `--serial` or `--demo`, open a repeater's own console |
| `--demo` | Run against a simulated mesh |
| `--theme NAME` | Choose a theme |
| `--motion full\|reduced\|off` | How much the app animates |
| `--ascii` | Draw in plain ASCII |
| `--config PATH` | Use a different config file |

`NO_COLOR` switches to the `mono` theme.
