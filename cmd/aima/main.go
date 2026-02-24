package main

import (
	"fmt"
	"os"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "aima: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// Will be wired up with Cobra CLI in Phase 3
	fmt.Println("AIMA — AI-Inference-Managed-by-AI")
	return nil
}
