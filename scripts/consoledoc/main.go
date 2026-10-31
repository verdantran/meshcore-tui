// Command consoledoc rewrites docs/console.md from the consoles' command tables: go run ./scripts/consoledoc.
package main

import (
	"log"
	"os"

	"github.com/verdantran/meshcore-tui/internal/console"
)

func main() {
	if err := os.WriteFile("docs/console.md", []byte(console.Markdown()), 0o644); err != nil {
		log.Fatal(err)
	}
}
