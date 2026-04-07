package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/jguan/aima/internal/central"
)

func main() {
	cfg := central.Config{
		Addr:     envOr("CENTRAL_ADDR", ":8080"),
		APIKey:   os.Getenv("CENTRAL_API_KEY"),
		DBPath:   envOr("CENTRAL_DB", "central.db"),
		DBDriver: envOr("CENTRAL_DB_DRIVER", "sqlite"),
		DBDSN:    os.Getenv("CENTRAL_DB_DSN"),
	}

	srv, err := central.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "central: %v\n", err)
		os.Exit(1)
	}
	defer srv.Close()

	// Set up advisor and analyzer if LLM is configured
	if llmURL := os.Getenv("LLM_BASE_URL"); llmURL != "" {
		llmKey := os.Getenv("LLM_API_KEY")
		llmModel := envOr("LLM_MODEL", "gpt-4")

		completer := central.NewOpenAICompleter(llmURL, llmKey, central.WithOpenAIModel(llmModel))

		advisor := central.NewAdvisor(srv.Store(), completer)
		srv.SetAdvisor(advisor)
		slog.Info("advisor enabled", "llm_url", llmURL, "model", llmModel)

		analyzer := central.NewAnalyzer(srv.Store(), completer)
		srv.SetAnalyzer(analyzer)
		analyzer.Start(context.Background())
		defer analyzer.Stop()
		slog.Info("analyzer started")
	} else {
		slog.Info("advisor/analyzer disabled: LLM_BASE_URL not set")
	}

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
