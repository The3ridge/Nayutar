package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/Kaikei-e/Nayutar/core"
)

func main() {
	root := flag.String("root", ".", "repository root")
	flag.Parse()
	if err := core.CheckProtectedTheorems(*root); err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err.Error())
		os.Exit(1)
	}
}
