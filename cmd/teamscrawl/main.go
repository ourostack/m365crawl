// Command teamscrawl mirrors the Microsoft Teams desktop cache into SQLite.
package main

import (
	"os"

	"github.com/ourostack/teamscrawl/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr))
}
