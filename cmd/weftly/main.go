package main

import (
	"fmt"
	"os"

	"github.com/bkum/weftly/internal/cli"
)

func main() {
	if err := cli.NewRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "weftly:", err)
		os.Exit(exitCode(err))
	}
}

func exitCode(err error) int {
	if c, ok := err.(interface{ ExitCode() int }); ok {
		return c.ExitCode()
	}
	return 1
}
