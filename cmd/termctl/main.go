// Command termctl drives terminal panes through backends.
package main

import (
	"os"

	"github.com/samhvw8/termctl/internal/cli"
)

func main() { os.Exit(cli.NewApp().Termctl(os.Args[1:])) }
