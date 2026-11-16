// Command vend is the CLI of the vending-machine platform.
package main

import (
	"flag"
	"fmt"
	"os"
)

// version is replaced at release time via ldflags.
const version = "0.1.0-m0"

func usage() {
	fmt.Fprintf(os.Stderr, "usage: vend <command> [options]\n\ncommands:\n  version [--json]  print version\n")
}

func cmdVersion(args []string) int {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	jsonOut := fs.Bool("json", false, "print version as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "vend version: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	if *jsonOut {
		fmt.Printf("{\"version\":%q}\n", version)
		return 0
	}
	fmt.Printf("vend %s\n", version)
	return 0
}

func run(args []string) int {
	if len(args) < 1 {
		usage()
		return 2
	}
	switch args[0] {
	case "version":
		return cmdVersion(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "vend: unknown command %q\n", args[0])
		usage()
		return 2
	}
}

func main() {
	os.Exit(run(os.Args[1:]))
}
