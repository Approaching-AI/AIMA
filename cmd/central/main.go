package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

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
	llmEndpoint := firstEnv("CENTRAL_LLM_ENDPOINT", "LLM_BASE_URL")
	llmKey := firstEnv("CENTRAL_LLM_API_KEY", "LLM_API_KEY")
	llmModel := firstEnv("CENTRAL_LLM_MODEL", "LLM_MODEL")
	if llmModel == "" {
		llmModel = "gpt-4"
	}

	if llmEndpoint != "" {
		opts := []central.OpenAIOption{central.WithOpenAIModel(llmModel)}
		if hdrs := os.Getenv("CENTRAL_LLM_HEADERS"); hdrs != "" {
			headers := make(map[string]string)
			for _, pair := range strings.Split(hdrs, ",") {
				if k, v, ok := strings.Cut(pair, "="); ok {
					headers[strings.TrimSpace(k)] = strings.TrimSpace(v)
				}
			}
			opts = append(opts, central.WithOpenAIHeaders(headers))
		}
		completer := central.NewOpenAICompleter(llmEndpoint, llmKey, opts...)

		advisor := central.NewAdvisor(srv.Store(), completer)
		srv.SetAdvisor(advisor)
		slog.Info("advisor enabled", "llm_endpoint", llmEndpoint, "model", llmModel)

		if envBool("CENTRAL_ANALYZER_ENABLED", true) {
			analyzerCfg := central.AnalyzerConfig{
				InitialDelay:           envDuration("CENTRAL_ANALYZER_INITIAL_DELAY", 30*time.Second),
				GapScanInterval:        envDuration("CENTRAL_ANALYZER_GAP_INTERVAL", 24*time.Hour),
				PatternInterval:        envDuration("CENTRAL_ANALYZER_PATTERN_INTERVAL", 7*24*time.Hour),
				ScenarioHealthInterval: envDuration("CENTRAL_ANALYZER_SCENARIO_INTERVAL", 7*24*time.Hour),
				PostIngestDelay:        envDuration("CENTRAL_ANALYZER_POST_INGEST_DELAY", 5*time.Minute),
				AdvisoryTTL:            envDuration("CENTRAL_ANALYZER_ADVISORY_TTL", 30*24*time.Hour),
			}
			analyzer := central.NewAnalyzer(srv.Store(), completer, central.WithAnalyzerConfig(analyzerCfg))
			srv.SetAnalyzer(analyzer)
			analyzer.Start(context.Background())
			defer analyzer.Stop()
			slog.Info("analyzer started",
				"gap_interval", analyzerCfg.GapScanInterval,
				"pattern_interval", analyzerCfg.PatternInterval,
				"scenario_interval", analyzerCfg.ScenarioHealthInterval,
				"post_ingest_delay", analyzerCfg.PostIngestDelay)
		} else {
			slog.Info("analyzer disabled", "env", "CENTRAL_ANALYZER_ENABLED")
		}
	} else {
		slog.Info("advisor/analyzer disabled: no LLM endpoint configured")
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

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if v := os.Getenv(key); v != "" {
			return v
		}
	}
	return ""
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		parsed, err := time.ParseDuration(v)
		if err != nil {
			slog.Warn("invalid duration env, using fallback", "key", key, "value", v, "fallback", fallback)
			return fallback
		}
		return parsed
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	if v := os.Getenv(key); v != "" {
		parsed, err := strconv.ParseBool(strings.TrimSpace(v))
		if err != nil {
			slog.Warn("invalid bool env, using fallback", "key", key, "value", v, "fallback", fallback)
			return fallback
		}
		return parsed
	}
	return fallback
}
