// Package httpapi — транспортный слой: разбор запроса, вызов домена, формирование ответа.
// Бизнес-логики здесь нет.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"tasksapi/internal/task"
)

// Handler держит зависимости транспорта и реализует http.Handler.
type Handler struct {
	store *task.Store
	log   *slog.Logger
	mux   *http.ServeMux
}

// NewHandler собирает роутер и регистрирует маршруты.
func NewHandler(store *task.Store, log *slog.Logger) *Handler {
	h := &Handler{store: store, log: log, mux: http.NewServeMux()}
	h.mux.HandleFunc("POST /tasks", h.create)
	h.mux.HandleFunc("GET /tasks", h.list)
	h.mux.HandleFunc("GET /tasks/{id}", h.get)
	return h
}

// ServeHTTP делает Handler совместимым с http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

// createRequest — тело запроса на создание задачи.
type createRequest struct {
	Title string `json:"title"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, r.Context(), http.StatusBadRequest, "invalid json body")
		return
	}

	t, err := h.store.Create(r.Context(), req.Title)
	if err != nil {
		if errors.Is(err, task.ErrEmptyTitle) {
			h.writeError(w, r.Context(), http.StatusBadRequest, "title is required")
			return
		}
		h.writeError(w, r.Context(), http.StatusInternalServerError, "failed to create task")
		return
	}
	h.writeJSON(w, r.Context(), http.StatusCreated, t)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		h.writeError(w, r.Context(), http.StatusBadRequest, "id must be a number")
		return
	}

	t, err := h.store.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, task.ErrNotFound) {
			h.writeError(w, r.Context(), http.StatusNotFound, "task not found")
			return
		}
		h.writeError(w, r.Context(), http.StatusInternalServerError, "failed to get task")
		return
	}
	h.writeJSON(w, r.Context(), http.StatusOK, t)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	tasks, err := h.store.List(r.Context())
	if err != nil {
		h.writeError(w, r.Context(), http.StatusInternalServerError, "failed to list tasks")
		return
	}
	h.writeJSON(w, r.Context(), http.StatusOK, tasks)
}

// writeJSON сериализует значение и пишет его с заданным статусом.
func (h *Handler) writeJSON(w http.ResponseWriter, ctx context.Context, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		h.log.ErrorContext(ctx, "encode response", slog.Any("error", err))
	}
}

// errorResponse — единый формат тела ошибки.
type errorResponse struct {
	Error string `json:"error"`
}

func (h *Handler) writeError(w http.ResponseWriter, ctx context.Context, status int, msg string) {
	h.writeJSON(w, ctx, status, errorResponse{Error: msg})
}
