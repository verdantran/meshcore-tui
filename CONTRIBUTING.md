# Contributing

Thanks for wanting to help. Issues and pull requests are welcome; for anything large, open an issue first so we can agree on the shape of it.

## Building and testing

You need Go 1.26.10 or later; on a Mac, the Xcode command line tools too (the build uses cgo for serial ports and Bluetooth).

```sh
go build -trimpath ./cmd/meshcore-tui
go vet ./...
go test ./...
go run ./cmd/meshcore-tui --demo   # a simulated mesh, no radio needed
```

`go test -short ./...` skips the frame-time check, which is slow on a busy machine. CI also runs `gofmt -l .`, which must print nothing, and `govulncheck`. It runs on pull requests and on main, so a branch is tested once it has a pull request, and a change only to Markdown, `docs/` or the licence runs nothing.

## Tests

- UI tests drive the app with keys and read the screen back; the helpers are in `internal/ui` (`newHarness`, `h.keys`, `h.screen()`).
- Animations are checked against golden frames in `testdata/golden`. If you change how something looks, run `go test ./internal/... -run Golden -update`, then read the diff before committing it.
- Parsers that read data from the radio or the mesh have fuzz tests; run one with `go test ./internal/packets -run '^$' -fuzz FuzzDecode -fuzztime 60s`.
- Use the demo's made-up nodes and places in tests, never real node names, keys or locations.

## Generated files

- `THIRD_PARTY_NOTICES`: run `sh scripts/notices.sh` after adding or updating a dependency, or embedding third-party data. CI fails if it's out of date.
- `docs/themes/*.svg`: run `go run ./scripts/swatches` after changing a theme.
- `internal/mapview/world.json.gz`: `go generate ./internal/mapview` rebuilds the built-in world map.

## Style

- Match the code around you. Keep comments for what the code can't say: a non-obvious reason, a unit, a workaround.
- Text the app shows uses British spelling. Hint lines read `key verb · key verb`; log lines read `VERB  detail`.
- Treat anything from the radio, the mesh or a file as hostile: clean text with `internal/untrusted` before it can reach the terminal, and never send a saved secret without the checks around it.
- Commit messages say what changes for the person using the app, in a short subject and a few sentences of prose.

## Security

Please report security problems privately; see [SECURITY.md](SECURITY.md).
