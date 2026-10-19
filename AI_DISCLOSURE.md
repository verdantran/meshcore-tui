# AI disclosure

meshcore-tui was written with a lot of help from AI. Most of the code, tests and documentation were written by Claude, Anthropic's AI model, working in Claude Code under my direction: I decided what to build and how it should behave, tried it out, and asked for changes. Commits it helped with carry a `Co-Authored-By: Claude` line.

## What's been checked

- **Security reviews.** Several rounds, done by AI agents reading the code, looking at what someone on the mesh, a radio, a node you log in to or a file you're given could make the app do. Every finding was fixed, with a test that shows it stays fixed. [SECURITY.md](SECURITY.md) lists the main protections.
- **Performance reviews.** Two, measuring drawing, CPU, the database and long sessions on generated data, up to a million messages. The slow spots they found were fixed and measured again.
- **Tests.** Unit tests, tests that drive the app with keys and read the screen back, golden frames for the animations, and fuzz tests for everything that reads data from the radio or the mesh.
- **CI** checks formatting, vets, runs the tests on Linux, macOS and Windows, and runs `govulncheck` for known vulnerabilities in Go and the dependencies.

## What that doesn't mean

None of this is an independent audit. The reviews were done by the same kind of tool that wrote the code, and both can miss the same things. It runs day to day against real radios, but not every feature has been tried with every radio, connection and operating system.

So please do your own due diligence, especially before you trust the app with your radio's private key, repeater passwords or private channels. Read the parts you rely on, run it against hardware you don't mind resetting, and report anything you find (see [SECURITY.md](SECURITY.md)).

The app comes with no warranty, as the [LICENSE](LICENSE) says.
