# Security

## Reporting a problem

Please report security problems privately, through GitHub's **Report a vulnerability** button on this repository's Security tab, rather than in a public issue. Say what you found, how to reproduce it, and what it lets someone do. You'll get a reply as soon as I can manage; this is a project kept in spare time.

Fixes go into the newest version; there are no older release lines to patch.

## What counts

Anything in this app, for example:

- someone on the mesh, a node you log in to, a radio you connect to, or a file you're given making the app do something you didn't ask: run commands, write files, send your passwords or keys, read or show private traffic, or drive your terminal;
- a message, signature or name being shown as something it isn't;
- secrets reaching the screen, the log, exports or disk where they shouldn't.

Problems in MeshCore's firmware or protocol, or in the [meshcore-go](https://github.com/OwlShack/meshcore-go) SDK, belong with those projects, though a note here is welcome if the app could work around them.

## Privacy

[docs/privacy.md](docs/privacy.md) covers what's kept and where. In short: text from the mesh is stripped of control characters before it reaches your terminal; saved passwords only go to a radio that proves by signing that it's yours; private files are readable only by you; and the Packets screen never shows other people's private traffic.
