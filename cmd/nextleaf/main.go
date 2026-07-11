package main

import (
	"fmt"
	"os"
)

func greeting() string {
	return "Nextleaf is ready."
}

func main() {
	if _, err := fmt.Fprintln(os.Stdout, greeting()); err != nil {
		os.Exit(1)
	}
}
