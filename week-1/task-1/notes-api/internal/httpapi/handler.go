// Package httpapi — транспортный слой: превращает HTTP-запросы в вызовы
// сервиса note.Service и обратно. Здесь нет бизнес-логики, только разбор
// запроса, вызов сервиса и формирование ответа.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/swanden/ai-challenge-advanced/week-1/task-1/notes-api/internal/note"
)

// Handler держит зависимости транспортного слоя и реализует http.Handler
// через собранный роутер.
type Handler struct {
	svc *note.Service
	log *slog.Logger
	mux *http.ServeMux
}

// NewHandler собирает роутер и регистрирует маршруты. Возвращаемое значение
// готово к передаче в http.Server.
func NewHandler(svc *note.Service, log *slog.Logger) *Handler {
	h := &Handler{svc: svc, log: log, mux: http.NewServeMux()}
	h.mux.HandleFunc("POST /notes", h.create)
	h.mux.HandleFunc("GET /notes", h.list)
	h.mux.HandleFunc("GET /notes/{id}", h.get)
	h.mux.HandleFunc("PATCH /notes/{id}", h.update)
	return h
}

// ServeHTTP делает Handler совместимым с http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

// createRequest — тело запроса на создание заметки.
type createRequest struct {
	Text string `json:"text"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, r.Context(), http.StatusBadRequest, "invalid json body")
		return
	}

	n, err := h.svc.Create(r.Context(), req.Text)
	if err != nil {
		if errors.Is(err, note.ErrEmptyText) {
			h.writeError(w, r.Context(), http.StatusBadRequest, "text is required")
			return
		}
		h.writeError(w, r.Context(), http.StatusInternalServerError, "failed to create note")
		return
	}
	h.writeJSON(w, r.Context(), http.StatusCreated, n)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	n, err := h.svc.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, note.ErrNotFound) {
			h.writeError(w, r.Context(), http.StatusNotFound, "note not found")
			return
		}
		h.writeError(w, r.Context(), http.StatusInternalServerError, "failed to get note")
		return
	}
	h.writeJSON(w, r.Context(), http.StatusOK, n)
}

// updateRequest — тело запроса на частичное обновление заметки.
type updateRequest struct {
	Text string `json:"text"`
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	var req updateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, r.Context(), http.StatusBadRequest, "invalid json body")
		return
	}

	n, err := h.svc.Update(r.Context(), r.PathValue("id"), req.Text)
	if err != nil {
		if errors.Is(err, note.ErrNotFound) {
			h.writeError(w, r.Context(), http.StatusNotFound, "note not found")
			return
		}
		if errors.Is(err, note.ErrEmptyText) {
			h.writeError(w, r.Context(), http.StatusBadRequest, "text is required")
			return
		}
		h.writeError(w, r.Context(), http.StatusInternalServerError, "failed to update note")
		return
	}
	h.writeJSON(w, r.Context(), http.StatusOK, n)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	notes, err := h.svc.List(r.Context())
	if err != nil {
		h.writeError(w, r.Context(), http.StatusInternalServerError, "failed to list notes")
		return
	}
	h.writeJSON(w, r.Context(), http.StatusOK, notes)
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
