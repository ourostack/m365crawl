// Command m365crawl mirrors the Microsoft Teams desktop cache into SQLite.
package main

import (
	"os"

	"github.com/ourostack/m365crawl/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr))
}
