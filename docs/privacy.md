# Privacy

meshcore-tui keeps what it needs on your own computer and sends nothing anywhere you didn't ask it to. This page says what that is.

## What's kept on your computer

| What | Where | For how long |
|---|---|---|
| Message history, for channels and direct messages | the data folder's `meshcore-tui.db` | Kept; leaving a channel deletes its history, and removing a contact keeps theirs |
| Contacts and channels as your radio last reported them | the same database | Replaced each time you connect |
| Names you've pinned to signing keys | the same database | Until you unpin them |
| Status history for nodes you watch | the same database | 90 days |
| Settings, saved devices and their keys, locations you placed by hand | `config.toml` | Until you change them |
| Repeater and room passwords you chose to save | the OS keychain, or `passwords.json` beside the config where there's none | Until you forget them (`x` on the Admin list) |
| Street detail for your mesh's area, once downloaded | the data folder's `maps` | Until `:map remove` |
| Exports | the data folder's `exports` | Until you delete them |

The data folder is `~/.local/share/meshcore-tui/` on macOS and Linux and `%LOCALAPPDATA%\meshcore-tui\` on Windows; the config is in `~/.config/meshcore-tui/` or `%APPDATA%\meshcore-tui\`. On macOS and Linux both folders and everything in them are readable only by you, and the app refuses to keep private files in a folder anyone else could write to. On Windows they take their permissions from your user profile.

Nothing is encrypted at rest beyond what your disk does: anyone who can read your files can read your history. Exports are plain text or JSON.

## What leaves your computer

- **To your radio:** messages you send, settings you change, and, when you log in to a repeater or room server, its password. Saved passwords only go to a radio that has proved, by signing, that it's the one you saved, and never without asking over WiFi or a Bluetooth link that may not be encrypted.
- **Over WiFi** the link to the radio isn't encrypted, so anyone on that network can read what passes, channel secrets included. USB and Bluetooth paired with the radio's PIN don't have that problem.
- **Street detail** is downloaded only when you press `d` on the map.

There are no accounts, analytics, crash reports or update checks.

## Notifications

Desktop notifications for direct messages and mentions can appear on a locked screen. `show` under `[notifications]` decides how much they carry: the message, only who it's from, or only that something arrived. See [configuration](configuration.md#desktop-notifications).

## The clipboard

Copying a private key or a channel's share link puts it on the clipboard, which clipboard managers and terminal multiplexers may keep. The app clears it again after 30 seconds if nothing else has been copied since.
