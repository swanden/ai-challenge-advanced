// Command notes-api поднимает HTTP-сервис заметок. Точка входа отвечает только
// за сборку зависимостей (wiring), запуск сервера и корректное завершение.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/swanden/ai-challenge-advanced/week-1/task-5/notes-api/internal/httpapi"
	"github.com/swanden/ai-challenge-advanced/week-1/task-5/notes-api/internal/note"
	"github.com/swanden/ai-challenge-advanced/week-1/task-5/notes-api/internal/storage"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	repo := storage.NewMemoryRepo()
	svc := note.NewService(repo, note.RandomID{}, note.SystemClock{})
	handler := httpapi.NewHandler(svc, log)

	srv := &http.Server{
		Addr:              ":8080",
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Слушаем сигналы завершения, чтобы закрыть сервер аккуратно.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Info("server starting", slog.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server failed", slog.Any("error", err))
			stop()
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("graceful shutdown failed", slog.Any("error", err))
		os.Exit(1)
	}
}
