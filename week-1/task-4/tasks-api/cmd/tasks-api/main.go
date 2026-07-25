// Command tasks-api поднимает HTTP-сервис задач.
package main

import (
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"

	"tasksapi/internal/httpapi"
	"tasksapi/internal/task"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	store := task.NewStore()
	handler := httpapi.NewHandler(store, log)

	srv := &http.Server{
		Addr:              ":8090",
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Info("server starting", slog.String("addr", srv.Addr))
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("server failed", slog.Any("error", err))
		os.Exit(1)
	}
}
