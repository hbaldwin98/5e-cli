package main

import (
	"fmt"
	"os"

	"github.com/hbaldwin98/5e-cli/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		code := 1
		if e, ok := err.(interface{ ExitCode() int }); ok {
			code = e.ExitCode()
		}
		os.Exit(code)
	}
}
