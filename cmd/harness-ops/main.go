// Command harness-ops talks to coding-agent panes on top of the terminal driver.
package main

import (
	"os"

	"github.com/samhvw8/termctl/internal/cli"
)

func main() { os.Exit(cli.NewApp().Harness(os.Args[1:])) }
