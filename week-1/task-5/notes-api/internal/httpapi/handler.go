// Package httpapi — транспортный слой: превращает HTTP-запросы в вызовы
// сервиса note.Service и обратно. Здесь нет бизнес-логики, только разбор
// запроса, вызов сервиса и формирование ответа.
package httpapi

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/swanden/ai-challenge-advanced/week-1/task-5/notes-api/internal/note"
)

//go:embed web
var webFS embed.FS

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
	h.mux.HandleFunc("GET /notes/count", h.count)
	h.mux.HandleFunc("GET /notes/{id}", h.get)
	h.mux.HandleFunc("PATCH /notes/{id}", h.update)
	h.mux.HandleFunc("DELETE /notes/{id}", h.delete)
	h.mux.HandleFunc("GET /healthz", h.healthz)

	sub, _ := fs.Sub(webFS, "web")
	h.mux.Handle("GET /", http.FileServerFS(sub))
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
	if !h.decodeJSON(w, r, &req) {
		return
	}

	n, err := h.svc.Create(r.Context(), req.Text)
	if err != nil {
		h.writeNoteError(w, r.Context(), err, "text is required", "", "failed to create note")
		return
	}
	h.writeJSON(w, r.Context(), http.StatusCreated, n)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	n, err := h.svc.Get(r.Context(), id)
	if err != nil {
		h.writeNoteError(w, r.Context(), err, "", "note not found", "failed to get note")
		return
	}
	h.writeJSON(w, r.Context(), http.StatusOK, n)
}

type updateRequest struct {
	Text string `json:"text"`
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req updateRequest
	if !h.decodeJSON(w, r, &req) {
		return
	}
	n, err := h.svc.UpdateText(r.Context(), id, req.Text)
	if err != nil {
		h.writeNoteError(w, r.Context(), err, "text is required", "note not found", "failed to update note")
		return
	}
	h.writeJSON(w, r.Context(), http.StatusOK, n)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.svc.Delete(r.Context(), id); err != nil {
		h.writeNoteError(w, r.Context(), err, "", "note not found", "failed to delete note")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	notes, err := h.svc.List(r.Context(), q)
	if err != nil {
		h.writeError(w, r.Context(), http.StatusInternalServerError, "failed to list notes")
		return
	}
	h.writeJSON(w, r.Context(), http.StatusOK, notes)
}

type healthzResponse struct {
	Status string `json:"status"`
}

func (h *Handler) healthz(w http.ResponseWriter, r *http.Request) {
	h.writeJSON(w, r.Context(), http.StatusOK, healthzResponse{Status: "ok"})
}

type countResponse struct {
	Count int `json:"count"`
}

func (h *Handler) count(w http.ResponseWriter, r *http.Request) {
	n, err := h.svc.Count(r.Context())
	if err != nil {
		h.writeError(w, r.Context(), http.StatusInternalServerError, "failed to count notes")
		return
	}
	h.writeJSON(w, r.Context(), http.StatusOK, countResponse{Count: n})
}

// decodeJSON декодирует тело запроса в v. При ошибке сама пишет 400 и
// возвращает false — вызывающему остаётся только вернуться из хендлера.
func (h *Handler) decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		h.writeError(w, r.Context(), http.StatusBadRequest, "invalid json body")
		return false
	}
	return true
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

// writeNoteError мапит доменную ошибку note в HTTP-статус и пишет ответ.
// emptyTextMsg и notFoundMsg — тексты для ErrEmptyText и ErrNotFound; пустая
// строка означает, что хендлер не ожидает такую ошибку от сервиса и она
// попадёт в fallback. fallbackMsg — текст для всех остальных ошибок (500).
func (h *Handler) writeNoteError(w http.ResponseWriter, ctx context.Context, err error, emptyTextMsg, notFoundMsg, fallbackMsg string) {
	switch {
	case emptyTextMsg != "" && errors.Is(err, note.ErrEmptyText):
		h.writeError(w, ctx, http.StatusBadRequest, emptyTextMsg)
	case notFoundMsg != "" && errors.Is(err, note.ErrNotFound):
		h.writeError(w, ctx, http.StatusNotFound, notFoundMsg)
	default:
		h.writeError(w, ctx, http.StatusInternalServerError, fallbackMsg)
	}
}
