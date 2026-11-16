// Command vend is the CLI of the vending-machine platform.
package main

import (
	"os"

	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/cli"
)

// version is replaced at release time via ldflags.
const version = "0.1.0-m0"

func main() {
	os.Exit(cli.Run(cli.Env{
		Args:    os.Args[1:],
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Getenv:  os.Getenv,
		Version: version,
	}))
}
