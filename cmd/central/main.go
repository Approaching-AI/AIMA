package main

import (
	"fmt"
	"os"

	"github.com/jguan/aima/internal/central"
)

func main() {
	cfg := central.Config{
		Addr:   envOr("CENTRAL_ADDR", ":8080"),
		APIKey: os.Getenv("CENTRAL_API_KEY"),
		DBPath: envOr("CENTRAL_DB", "central.db"),
	}

	srv, err := central.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "central: %v\n", err)
		os.Exit(1)
	}
	defer srv.Close()

	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintf(os.Stderr, "central: %v\n", err)
		os.Exit(1)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
