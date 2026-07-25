package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/swanden/ai-challenge-advanced/week-1/task-3/notes-api/internal/note"
)

// stubRepo — тестовый дубль note.Repository с настраиваемым поведением:
// хранит заметки в мапе и умеет возвращать заданную ошибку на любом методе.
type stubRepo struct {
	notes   map[string]note.Note
	err     error // ошибка, которую вернёт любой метод, если задана
	gotCtx  context.Context
	created []note.Note
}

func newStubRepo() *stubRepo { return &stubRepo{notes: make(map[string]note.Note)} }

func (r *stubRepo) Create(ctx context.Context, n note.Note) error {
	r.gotCtx = ctx
	if r.err != nil {
		return r.err
	}
	r.notes[n.ID] = n
	r.created = append(r.created, n)
	return nil
}

func (r *stubRepo) Get(ctx context.Context, id string) (note.Note, error) {
	r.gotCtx = ctx
	if r.err != nil {
		return note.Note{}, r.err
	}
	n, ok := r.notes[id]
	if !ok {
		return note.Note{}, note.ErrNotFound
	}
	return n, nil
}

func (r *stubRepo) Update(ctx context.Context, n note.Note) error {
	r.gotCtx = ctx
	if r.err != nil {
		return r.err
	}
	if _, ok := r.notes[n.ID]; !ok {
		return note.ErrNotFound
	}
	r.notes[n.ID] = n
	return nil
}

func (r *stubRepo) Delete(ctx context.Context, id string) error {
	r.gotCtx = ctx
	if r.err != nil {
		return r.err
	}
	if _, ok := r.notes[id]; !ok {
		return note.ErrNotFound
	}
	delete(r.notes, id)
	return nil
}

func (r *stubRepo) List(ctx context.Context) ([]note.Note, error) {
	r.gotCtx = ctx
	if r.err != nil {
		return nil, r.err
	}
	out := make([]note.Note, 0, len(r.notes))
	for _, n := range r.notes {
		out = append(out, n)
	}
	return out, nil
}

type stubID struct{ id string }

func (s stubID) NewID() string { return s.id }

type stubClock struct{ t time.Time }

func (s stubClock) Now() time.Time { return s.t }

var testTime = time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)

// newTestHandler собирает Handler поверх подставного репозитория и логгера в буфер.
func newTestHandler(repo note.Repository) (*Handler, *bytes.Buffer) {
	var logBuf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logBuf, nil))
	svc := note.NewService(repo, stubID{id: "id-1"}, stubClock{t: testTime})
	return NewHandler(svc, log), &logBuf
}

func decodeError(t *testing.T, body *bytes.Buffer) errorResponse {
	t.Helper()
	var got errorResponse
	if err := json.Unmarshal(body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal error body %q: %v", body.String(), err)
	}
	return got
}

func TestHandlerCreate(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		repoErr    error
		wantStatus int
		wantErrMsg string
		wantText   string
	}{
		{
			name:       "valid body creates note",
			body:       `{"text":"  hello  "}`,
			wantStatus: http.StatusCreated,
			wantText:   "hello",
		},
		{
			name:       "broken json is 400",
			body:       `{"text":`,
			wantStatus: http.StatusBadRequest,
			wantErrMsg: "invalid json body",
		},
		{
			name:       "wrong field type is 400",
			body:       `{"text":42}`,
			wantStatus: http.StatusBadRequest,
			wantErrMsg: "invalid json body",
		},
		{
			name:       "empty body is 400",
			body:       ``,
			wantStatus: http.StatusBadRequest,
			wantErrMsg: "invalid json body",
		},
		{
			name:       "blank text is 400 from domain",
			body:       `{"text":"   "}`,
			wantStatus: http.StatusBadRequest,
			wantErrMsg: "text is required",
		},
		{
			name:       "missing text field is 400 from domain",
			body:       `{}`,
			wantStatus: http.StatusBadRequest,
			wantErrMsg: "text is required",
		},
		{
			name:       "repository failure is 500",
			body:       `{"text":"hello"}`,
			repoErr:    errors.New("db is down"),
			wantStatus: http.StatusInternalServerError,
			wantErrMsg: "failed to create note",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newStubRepo()
			repo.err = tt.repoErr
			h, _ := newTestHandler(repo)

			req := httptest.NewRequest(http.MethodPost, "/notes", strings.NewReader(tt.body))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}
			if tt.wantErrMsg != "" {
				got := decodeError(t, rec.Body)
				if got.Error != tt.wantErrMsg {
					t.Fatalf("error = %q, want %q", got.Error, tt.wantErrMsg)
				}
				if strings.Contains(got.Error, "db is down") {
					t.Errorf("internal error leaked to client: %q", got.Error)
				}
				return
			}

			var got note.Note
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("unmarshal note: %v", err)
			}
			if got.Text != tt.wantText {
				t.Errorf("Text = %q, want %q", got.Text, tt.wantText)
			}
			if got.ID != "id-1" {
				t.Errorf("ID = %q, want %q", got.ID, "id-1")
			}
			if !got.CreatedAt.Equal(testTime) || !got.UpdatedAt.Equal(testTime) {
				t.Errorf("timestamps = %v/%v, want %v", got.CreatedAt, got.UpdatedAt, testTime)
			}
			if len(repo.created) != 1 {
				t.Fatalf("repo.Create called %d times, want 1", len(repo.created))
			}
		})
	}
}

func TestHandlerGet(t *testing.T) {
	stored := note.Note{ID: "id-1", Text: "stored", CreatedAt: testTime, UpdatedAt: testTime}

	tests := []struct {
		name       string
		path       string
		seed       bool
		repoErr    error
		wantStatus int
		wantErrMsg string
	}{
		{name: "existing note is 200", path: "/notes/id-1", seed: true, wantStatus: http.StatusOK},
		{name: "unknown id is 404", path: "/notes/nope", wantStatus: http.StatusNotFound, wantErrMsg: "note not found"},
		{
			name:       "repository failure is 500",
			path:       "/notes/id-1",
			repoErr:    errors.New("db is down"),
			wantStatus: http.StatusInternalServerError,
			wantErrMsg: "failed to get note",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newStubRepo()
			if tt.seed {
				repo.notes[stored.ID] = stored
			}
			repo.err = tt.repoErr
			h, _ := newTestHandler(repo)

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if tt.wantErrMsg != "" {
				if got := decodeError(t, rec.Body); got.Error != tt.wantErrMsg {
					t.Fatalf("error = %q, want %q", got.Error, tt.wantErrMsg)
				}
				return
			}

			var got note.Note
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("unmarshal note: %v", err)
			}
			if got.ID != stored.ID || got.Text != stored.Text {
				t.Errorf("note = %+v, want %+v", got, stored)
			}
		})
	}
}

// TestHandlerUpdate покрывает PATCH /notes/{id}: успешное обновление текста
// и все ветки ошибок, которые хендлер обязан различать по errors.Is.
func TestHandlerUpdate(t *testing.T) {
	createdAt := testTime.Add(-time.Hour)
	stored := note.Note{ID: "id-1", Text: "stored", CreatedAt: createdAt, UpdatedAt: createdAt}

	tests := []struct {
		name       string
		path       string
		body       string
		repoErr    error
		wantStatus int
		wantErrMsg string
		wantText   string
	}{
		{
			name:       "valid body updates text",
			path:       "/notes/id-1",
			body:       `{"text":"  updated  "}`,
			wantStatus: http.StatusOK,
			wantText:   "updated",
		},
		{
			name:       "broken json is 400",
			path:       "/notes/id-1",
			body:       `{"text":`,
			wantStatus: http.StatusBadRequest,
			wantErrMsg: "invalid json body",
		},
		{
			name:       "wrong field type is 400",
			path:       "/notes/id-1",
			body:       `{"text":42}`,
			wantStatus: http.StatusBadRequest,
			wantErrMsg: "invalid json body",
		},
		{
			name:       "empty body is 400",
			path:       "/notes/id-1",
			body:       ``,
			wantStatus: http.StatusBadRequest,
			wantErrMsg: "invalid json body",
		},
		{
			name:       "blank text is 400 from domain",
			path:       "/notes/id-1",
			body:       `{"text":"   "}`,
			wantStatus: http.StatusBadRequest,
			wantErrMsg: "text is required",
		},
		{
			name:       "missing text field is 400 from domain",
			path:       "/notes/id-1",
			body:       `{}`,
			wantStatus: http.StatusBadRequest,
			wantErrMsg: "text is required",
		},
		{
			name:       "blank text on unknown id is 400, not 404",
			path:       "/notes/nope",
			body:       `{"text":"   "}`,
			wantStatus: http.StatusBadRequest,
			wantErrMsg: "text is required",
		},
		{
			name:       "unknown id is 404",
			path:       "/notes/nope",
			body:       `{"text":"updated"}`,
			wantStatus: http.StatusNotFound,
			wantErrMsg: "note not found",
		},
		{
			name:       "repository failure is 500",
			path:       "/notes/id-1",
			body:       `{"text":"updated"}`,
			repoErr:    errors.New("db is down"),
			wantStatus: http.StatusInternalServerError,
			wantErrMsg: "failed to update note",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newStubRepo()
			repo.notes[stored.ID] = stored
			repo.err = tt.repoErr
			h, _ := newTestHandler(repo)

			req := httptest.NewRequest(http.MethodPatch, tt.path, strings.NewReader(tt.body))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}
			if tt.wantErrMsg != "" {
				got := decodeError(t, rec.Body)
				if got.Error != tt.wantErrMsg {
					t.Fatalf("error = %q, want %q", got.Error, tt.wantErrMsg)
				}
				if strings.Contains(got.Error, "db is down") {
					t.Errorf("internal error leaked to client: %q", got.Error)
				}
				if kept := repo.notes[stored.ID]; kept.Text != stored.Text {
					t.Errorf("stored Text = %q, want %q — заметка изменилась при ошибке", kept.Text, stored.Text)
				}
				return
			}

			var got note.Note
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("unmarshal note: %v", err)
			}
			if got.ID != stored.ID {
				t.Errorf("ID = %q, want %q", got.ID, stored.ID)
			}
			if got.Text != tt.wantText {
				t.Errorf("Text = %q, want %q", got.Text, tt.wantText)
			}
			if !got.CreatedAt.Equal(createdAt) {
				t.Errorf("CreatedAt = %v, want %v — время создания менять нельзя", got.CreatedAt, createdAt)
			}
			if !got.UpdatedAt.Equal(testTime) {
				t.Errorf("UpdatedAt = %v, want %v (время должно приходить из Clock)", got.UpdatedAt, testTime)
			}
			if !got.UpdatedAt.After(got.CreatedAt) {
				t.Errorf("UpdatedAt (%v) не сдвинулся относительно CreatedAt (%v)", got.UpdatedAt, got.CreatedAt)
			}
			if kept := repo.notes[stored.ID]; kept != got {
				t.Errorf("repo has %+v, want %+v — ответ разошёлся с сохранённым", kept, got)
			}
		})
	}
}

func TestHandlerDelete(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		seed       bool
		repoErr    error
		wantStatus int
		wantErrMsg string
	}{
		{name: "existing note is 204 without body", path: "/notes/id-1", seed: true, wantStatus: http.StatusNoContent},
		{name: "unknown id is 404", path: "/notes/nope", wantStatus: http.StatusNotFound, wantErrMsg: "note not found"},
		{
			name:       "repository failure is 500",
			path:       "/notes/id-1",
			repoErr:    errors.New("db is down"),
			wantStatus: http.StatusInternalServerError,
			wantErrMsg: "failed to delete note",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newStubRepo()
			if tt.seed {
				repo.notes["id-1"] = note.Note{ID: "id-1", Text: "x"}
			}
			repo.err = tt.repoErr
			h, _ := newTestHandler(repo)

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, tt.path, nil))

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if tt.wantErrMsg != "" {
				if got := decodeError(t, rec.Body); got.Error != tt.wantErrMsg {
					t.Fatalf("error = %q, want %q", got.Error, tt.wantErrMsg)
				}
				return
			}
			if body := rec.Body.String(); body != "" {
				t.Errorf("body = %q, want empty for 204", body)
			}
			if _, ok := repo.notes["id-1"]; ok {
				t.Error("note still present in repository after successful delete")
			}
		})
	}
}

func TestHandlerList(t *testing.T) {
	t.Run("empty repository returns empty json array", func(t *testing.T) {
		h, _ := newTestHandler(newStubRepo())

		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/notes", nil))

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
			t.Fatalf("body = %q, want %q (null breaks JS clients)", got, "[]")
		}
	})

	t.Run("notes are returned", func(t *testing.T) {
		repo := newStubRepo()
		repo.notes["id-1"] = note.Note{ID: "id-1", Text: "one", CreatedAt: testTime}
		h, _ := newTestHandler(repo)

		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/notes", nil))

		var got []note.Note
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("unmarshal list: %v", err)
		}
		if len(got) != 1 || got[0].Text != "one" {
			t.Fatalf("list = %+v, want single note %q", got, "one")
		}
	})

	t.Run("repository failure is 500", func(t *testing.T) {
		repo := newStubRepo()
		repo.err = errors.New("db is down")
		h, _ := newTestHandler(repo)

		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/notes", nil))

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
		}
		if got := decodeError(t, rec.Body); got.Error != "failed to list notes" {
			t.Fatalf("error = %q, want %q", got.Error, "failed to list notes")
		}
	})
}

// TestHandlerPassesRequestContext фиксирует обязательный паттерн проекта:
// хендлер прокладывает r.Context() до репозитория, а не создаёт свой.
func TestHandlerPassesRequestContext(t *testing.T) {
	type ctxKey struct{}

	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "create", method: http.MethodPost, path: "/notes", body: `{"text":"hi"}`},
		{name: "get", method: http.MethodGet, path: "/notes/id-1", body: ""},
		{name: "update", method: http.MethodPatch, path: "/notes/id-1", body: `{"text":"hi"}`},
		{name: "list", method: http.MethodGet, path: "/notes", body: ""},
		{name: "delete", method: http.MethodDelete, path: "/notes/id-1", body: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newStubRepo()
			repo.notes["id-1"] = note.Note{ID: "id-1", Text: "x"}
			h, _ := newTestHandler(repo)

			var body io.Reader
			if tt.body != "" {
				body = strings.NewReader(tt.body)
			}
			req := httptest.NewRequest(tt.method, tt.path, body)
			req = req.WithContext(context.WithValue(req.Context(), ctxKey{}, "marker"))

			h.ServeHTTP(httptest.NewRecorder(), req)

			if repo.gotCtx == nil {
				t.Fatal("repository was not called")
			}
			if got := repo.gotCtx.Value(ctxKey{}); got != "marker" {
				t.Fatalf("repository ctx value = %v, want %q — request context was not propagated", got, "marker")
			}
		})
	}
}

// TestHandlerRouting проверяет карту маршрутов: методы, которых нет,
// не должны молча попадать в другой обработчик.
func TestHandlerRouting(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
	}{
		{name: "PUT /notes is not allowed", method: http.MethodPut, path: "/notes", wantStatus: http.StatusMethodNotAllowed},
		{name: "POST /notes/{id} is not allowed", method: http.MethodPost, path: "/notes/id-1", wantStatus: http.StatusMethodNotAllowed},
		// PATCH зарегистрирован: пустое тело доходит до хендлера и даёт 400, а не 405.
		{name: "PATCH /notes/{id} is routed", method: http.MethodPatch, path: "/notes/id-1", wantStatus: http.StatusBadRequest},
		{name: "PUT /notes/{id} is not routed", method: http.MethodPut, path: "/notes/id-1", wantStatus: http.StatusMethodNotAllowed},
		{name: "DELETE /notes/{id} is routed", method: http.MethodDelete, path: "/notes/id-1", wantStatus: http.StatusNotFound},
		{name: "DELETE /notes collection is not allowed", method: http.MethodDelete, path: "/notes", wantStatus: http.StatusMethodNotAllowed},
		{name: "GET / serves the web ui", method: http.MethodGet, path: "/", wantStatus: http.StatusOK},
		{name: "GET unknown static path is 404", method: http.MethodGet, path: "/nope.js", wantStatus: http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _ := newTestHandler(newStubRepo())

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tt.wantStatus, rec.Body.String())
			}
		})
	}
}

// TestHandlerServesEmbeddedIndex проверяет, что встроенная в бинарь страница
// действительно отдаётся по корневому маршруту.
func TestHandlerServesEmbeddedIndex(t *testing.T) {
	h, _ := newTestHandler(newStubRepo())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !strings.Contains(strings.ToLower(rec.Body.String()), "<html") {
		t.Fatalf("body does not look like the index page: %q", rec.Body.String())
	}
}

// failingWriter — ResponseWriter, у которого падает запись тела.
type failingWriter struct {
	header http.Header
	status int
}

func (w *failingWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}
func (w *failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }
func (w *failingWriter) WriteHeader(status int)    { w.status = status }

// TestWriteJSONLogsEncodeError фиксирует правило «ошибки не проглатываем»:
// сбой кодирования ответа должен попасть в лог с контекстом.
func TestWriteJSONLogsEncodeError(t *testing.T) {
	h, logBuf := newTestHandler(newStubRepo())

	w := &failingWriter{}
	h.writeJSON(w, context.Background(), http.StatusOK, note.Note{ID: "id-1"})

	if w.status != http.StatusOK {
		t.Errorf("status = %d, want %d", w.status, http.StatusOK)
	}
	if !strings.Contains(logBuf.String(), "encode response") {
		t.Fatalf("encode error was swallowed, log = %q", logBuf.String())
	}
	if !strings.Contains(logBuf.String(), "broken pipe") {
		t.Errorf("log has no underlying error: %q", logBuf.String())
	}
}
