package main

import (
	"os"

	"github.com/sosalejandro/testreg/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
