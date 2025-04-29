package main

import (
	"fmt"
	"os"

	"github.com/jessevdk/go-flags"
)

// ErrExtraArgs is returned  if extra arguments to a command are found
var ErrExtraArgs = fmt.Errorf("too many arguments for command")

type parseOptions struct {
	Verbose bool `short:"v" long:"verbose" description:"Verbosity level"`
}

var (
	opts   parseOptions
	parser = flags.NewParser(&opts, flags.Default)
)

func main() {
	if _, err := parser.Parse(); err != nil {
		switch flagsErr := err.(type) {
		case flags.ErrorType:
			if flagsErr == flags.ErrHelp {
				os.Exit(0)
			}
			os.Exit(1)
		default:
			os.Exit(1)
		}
	}
}
