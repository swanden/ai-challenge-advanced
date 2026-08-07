// Команда gateway поднимает прокси между приложением и провайдером модели.
//
// Запускать из корня репозитория:
//
//	go run ./week-3/task-13/cmd/gateway
//	go run ./week-3/task-13/cmd/gateway -input-mode block -rate 30
//
// После запуска раннеры Дней 11 и 12 идут через него подменой одного флага:
//
//	go run ./week-3/task-12/cmd/trap -base-url http://localhost:8787 ...
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/swanden/ai-challenge-advanced/week-3/task-13/internal/audit"
	"github.com/swanden/ai-challenge-advanced/week-3/task-13/internal/policy"
	"github.com/swanden/ai-challenge-advanced/week-3/task-13/internal/proxy"
)

func main() {
	var (
		addr       = flag.String("addr", ":8787", "адрес прокси")
		anthropic  = flag.String("anthropic", "https://api.anthropic.com", "апстрим Anthropic")
		openai     = flag.String("openai", "http://localhost:11434/v1", "апстрим OpenAI-совместимого API")
		inputMode  = flag.String("input-mode", "mask", "off, mask или block")
		outputMode = flag.String("output-mode", "mask", "off, mask или block")
		rate       = flag.Int("rate", 0, "предел запросов в минуту с клиента; 0 — без предела")
		logDir     = flag.String("logs", "week-3/task-13/logs", "каталог журнала")
	)
	flag.Parse()

	in, ok := parseMode(*inputMode)
	if !ok {
		fail("неизвестный -input-mode %q", *inputMode)
	}
	out, ok := parseMode(*outputMode)
	if !ok {
		fail("неизвестный -output-mode %q", *outputMode)
	}

	lg, err := audit.New(*logDir)
	if err != nil {
		fail("журнал: %v", err)
	}

	g := proxy.New(proxy.Config{
		AnthropicBase: *anthropic,
		OpenAIBase:    *openai,
		InputMode:     in,
		OutputMode:    out,
		RatePerMinute: *rate,
		Log:           lg,
		Client:        &http.Client{Timeout: 120 * time.Second},
	})

	fmt.Printf("LLM Gateway на %s\n", *addr)
	fmt.Printf("  вход: %s, выход: %s", in, out)
	if *rate > 0 {
		fmt.Printf(", предел %d запросов в минуту", *rate)
	}
	fmt.Printf("\n  апстримы: %s (Anthropic), %s (OpenAI-совместимый)\n", *anthropic, *openai)
	fmt.Printf("  журнал: %s\n", *logDir)
	fmt.Printf("  клиенту достаточно подменить base-url на http://localhost%s\n\n", *addr)

	srv := &http.Server{
		Addr:              *addr,
		Handler:           g.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Fatal(srv.ListenAndServe())
}

func parseMode(s string) (policy.Mode, bool) {
	switch policy.Mode(s) {
	case policy.ModeOff, policy.ModeMask, policy.ModeBlock:
		return policy.Mode(s), true
	}
	return "", false
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format, args...)
	fmt.Fprintln(os.Stderr)
	os.Exit(1)
}
