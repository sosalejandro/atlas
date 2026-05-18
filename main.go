package main

import (
	"os"

	"github.com/sosalejandro/atlas/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
