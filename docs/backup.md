# Backing up a radio

A backup saves your radio's settings, channels and contacts to a file, so you can put them back after a factory reset, or set up a new radio the same way.

## Making one

On the Device screen (`6`), pick **Back up this radio**, or type `:radio backup` anywhere. A list shows what can go in, with what each part holds:

| Part | What's in it |
|---|---|
| Name and location | The radio's name, and the location it advertises |
| Radio settings | Frequency, bandwidth, spreading factor, coding rate, TX power and tuning |
| Options | Telemetry sharing, auto-add, the path hash size, extra acks and the default flood scope |
| BLE PIN | The Bluetooth pairing PIN |
| Channels | Every channel, with its secret |
| Contacts | Every contact, with its stored route |
| Private key | The radio's identity |

`space` ticks or unticks a part, `a` ticks or unticks them all, and `enter` saves. Everything but the private key starts ticked; the key goes in only if you tick it, and then only after a `y`.

The file goes in your exports folder (`~/.local/share/meshcore-tui/exports` on macOS and Linux), named after the radio and the time, such as `radio-meshcore-k4-2026-10-23-091500.json`. It's readable only by you, and it's plain JSON, so you can read what's in it.

## Keep it safe

- **With the private key in it, the file is your radio's identity.** Anyone who has it can read your direct messages and send as you. Only include the key when you need to move the identity to another radio, keep the file somewhere private, and delete it when you're done.
- **Channel secrets are in it too**, so anyone with the file can read your private channels.

## Restoring

On the Device screen, pick **Restore a backup**, or type `:radio restore`, optionally followed by the file's path. The newest backup in your exports folder is filled in for you.

Nothing changes until you've chosen what to restore. The same list shows each part in the file and what it would change: the radio settings now and after, say, or `2 to add, 3 already here` for channels. The private key always starts unticked, even when the file has it. Type `restore` to go ahead.

- **Channels and contacts you already have are kept.** Only missing ones are added, so restoring never overwrites a channel or contact. Channels go into free slots.
- **A channel that would mislead is refused**, and the list says which and why: a `#hashtag` whose secret isn't the one its name gives, or a private channel with a secret anyone could guess.
- **New radio settings mean you'll only hear nodes that use them too**, as when changing them by hand.
- **Restoring the private key replaces the radio's identity.** Contacts will see a different node, and the current key is gone unless you've backed it up.

Each part is sent in turn, the private key last. If one fails, the rest still go ahead, and the log says which failed.

A backup comes from a file you're given as much as one you made, so every value in it is checked before anything reaches the radio. A file that isn't a backup, or has anything out of range, is refused.
