package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/swanden/ai-challenge-advanced/week-1/task-3/notes-api/internal/note"
)

// doRequest прогоняет запрос через собранный Handler и возвращает ответ.
func doRequest(t *testing.T, h *Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// decodeObject разбирает тело в map, чтобы проверять именно проводной формат
// (имена полей), а не то, как их видит Go-структура.
func decodeObject(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal object %q: %v", raw, err)
	}
	return got
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// wantNoteKeys — контракт заметки на проводе. Фронтенд (web/index.html)
// читает именно эти имена, поэтому переименование поля — поломка API.
var wantNoteKeys = map[string]struct{}{
	"id":         {},
	"text":       {},
	"created_at": {},
	"updated_at": {},
}

func assertNoteShape(t *testing.T, obj map[string]any) {
	t.Helper()
	for k := range wantNoteKeys {
		if _, ok := obj[k]; !ok {
			t.Fatalf("в ответе нет поля %q, есть только %v", k, keysOf(obj))
		}
	}
	for k := range obj {
		if _, ok := wantNoteKeys[k]; !ok {
			t.Fatalf("в ответе лишнее поле %q (ключи: %v)", k, keysOf(obj))
		}
	}
	for _, k := range []string{"id", "text", "created_at", "updated_at"} {
		if _, ok := obj[k].(string); !ok {
			t.Fatalf("поле %q = %#v, want string", k, obj[k])
		}
	}
}

// assertUTCTimestamp фиксирует правило «время всегда UTC»: на проводе это
// RFC3339 с суффиксом Z, а не локальное время со смещением.
func assertUTCTimestamp(t *testing.T, field, value string, want time.Time) {
	t.Helper()
	if !strings.HasSuffix(value, "Z") {
		t.Fatalf("%s = %q, want RFC3339 в UTC (суффикс Z)", field, value)
	}
	got, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatalf("%s = %q не парсится как RFC3339: %v", field, value, err)
	}
	if !got.Equal(want) {
		t.Fatalf("%s = %v, want %v", field, got, want)
	}
}

// TestNoteJSONContract фиксирует проводной формат заметки на всех эндпоинтах,
// которые её отдают: набор полей, их типы и формат времени.
func TestNoteJSONContract(t *testing.T) {
	stored := note.Note{ID: "id-1", Text: "stored", CreatedAt: testTime, UpdatedAt: testTime}

	tests := []struct {
		name       string
		method     string
		path       string
		body       string
		seed       bool
		wantStatus int
		wantText   string
	}{
		{
			name:       "POST /notes",
			method:     http.MethodPost,
			path:       "/notes",
			body:       `{"text":"hello"}`,
			wantStatus: http.StatusCreated,
			wantText:   "hello",
		},
		{
			name:       "GET /notes/{id}",
			method:     http.MethodGet,
			path:       "/notes/id-1",
			seed:       true,
			wantStatus: http.StatusOK,
			wantText:   "stored",
		},
		{
			name:       "PATCH /notes/{id}",
			method:     http.MethodPatch,
			path:       "/notes/id-1",
			body:       `{"text":"updated"}`,
			seed:       true,
			wantStatus: http.StatusOK,
			wantText:   "updated",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newStubRepo()
			if tt.seed {
				repo.notes[stored.ID] = stored
			}
			h, _ := newTestHandler(repo)

			rec := doRequest(t, h, tt.method, tt.path, tt.body)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tt.wantStatus, rec.Body.String())
			}

			obj := decodeObject(t, rec.Body.Bytes())
			assertNoteShape(t, obj)
			if obj["text"] != tt.wantText {
				t.Errorf("text = %q, want %q", obj["text"], tt.wantText)
			}
			if obj["id"] != "id-1" {
				t.Errorf("id = %q, want %q", obj["id"], "id-1")
			}
			assertUTCTimestamp(t, "updated_at", obj["updated_at"].(string), testTime)
		})
	}
}

// TestListJSONContract проверяет, что список — это JSON-массив объектов
// того же формата, а пустой список сериализуется как [], а не null.
func TestListJSONContract(t *testing.T) {
	tests := []struct {
		name    string
		seed    []note.Note
		wantLen int
	}{
		{name: "empty list", wantLen: 0},
		{
			name:    "single note",
			seed:    []note.Note{{ID: "id-1", Text: "one", CreatedAt: testTime, UpdatedAt: testTime}},
			wantLen: 1,
		},
		{
			name: "two notes",
			seed: []note.Note{
				{ID: "id-1", Text: "one", CreatedAt: testTime, UpdatedAt: testTime},
				{ID: "id-2", Text: "two", CreatedAt: testTime, UpdatedAt: testTime},
			},
			wantLen: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newStubRepo()
			for _, n := range tt.seed {
				repo.notes[n.ID] = n
			}
			h, _ := newTestHandler(repo)

			rec := doRequest(t, h, http.MethodGet, "/notes", "")
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}

			raw := strings.TrimSpace(rec.Body.String())
			if !strings.HasPrefix(raw, "[") || !strings.HasSuffix(raw, "]") {
				t.Fatalf("body = %q, want JSON-массив", raw)
			}

			var items []map[string]any
			if err := json.Unmarshal([]byte(raw), &items); err != nil {
				t.Fatalf("unmarshal list %q: %v", raw, err)
			}
			if items == nil {
				t.Fatal("list = null, want [] (null ломает JS-клиента)")
			}
			if len(items) != tt.wantLen {
				t.Fatalf("len(list) = %d, want %d", len(items), tt.wantLen)
			}
			for _, obj := range items {
				assertNoteShape(t, obj)
				assertUTCTimestamp(t, "created_at", obj["created_at"].(string), testTime)
			}
		})
	}
}

// TestErrorJSONContract фиксирует единый формат ошибки: ровно одно поле
// "error" со строкой и никаких внутренних деталей наружу.
func TestErrorJSONContract(t *testing.T) {
	internal := "top secret dsn user:password@db"

	tests := []struct {
		name       string
		method     string
		path       string
		body       string
		repoErr    error
		wantStatus int
		wantMsg    string
	}{
		{
			name:       "create invalid json",
			method:     http.MethodPost,
			path:       "/notes",
			body:       `{`,
			wantStatus: http.StatusBadRequest,
			wantMsg:    "invalid json body",
		},
		{
			name:       "create empty text",
			method:     http.MethodPost,
			path:       "/notes",
			body:       `{"text":" "}`,
			wantStatus: http.StatusBadRequest,
			wantMsg:    "text is required",
		},
		{
			name:       "get missing note",
			method:     http.MethodGet,
			path:       "/notes/nope",
			wantStatus: http.StatusNotFound,
			wantMsg:    "note not found",
		},
		{
			name:       "update missing note",
			method:     http.MethodPatch,
			path:       "/notes/nope",
			body:       `{"text":"x"}`,
			wantStatus: http.StatusNotFound,
			wantMsg:    "note not found",
		},
		{
			name:       "delete missing note",
			method:     http.MethodDelete,
			path:       "/notes/nope",
			wantStatus: http.StatusNotFound,
			wantMsg:    "note not found",
		},
		{
			name:       "create repo failure",
			method:     http.MethodPost,
			path:       "/notes",
			body:       `{"text":"x"}`,
			repoErr:    errInternal(internal),
			wantStatus: http.StatusInternalServerError,
			wantMsg:    "failed to create note",
		},
		{
			name:       "get repo failure",
			method:     http.MethodGet,
			path:       "/notes/id-1",
			repoErr:    errInternal(internal),
			wantStatus: http.StatusInternalServerError,
			wantMsg:    "failed to get note",
		},
		{
			name:       "update repo failure",
			method:     http.MethodPatch,
			path:       "/notes/id-1",
			body:       `{"text":"x"}`,
			repoErr:    errInternal(internal),
			wantStatus: http.StatusInternalServerError,
			wantMsg:    "failed to update note",
		},
		{
			name:       "delete repo failure",
			method:     http.MethodDelete,
			path:       "/notes/id-1",
			repoErr:    errInternal(internal),
			wantStatus: http.StatusInternalServerError,
			wantMsg:    "failed to delete note",
		},
		{
			name:       "list repo failure",
			method:     http.MethodGet,
			path:       "/notes",
			repoErr:    errInternal(internal),
			wantStatus: http.StatusInternalServerError,
			wantMsg:    "failed to list notes",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newStubRepo()
			repo.notes["id-1"] = note.Note{ID: "id-1", Text: "stored", CreatedAt: testTime, UpdatedAt: testTime}
			repo.err = tt.repoErr
			h, _ := newTestHandler(repo)

			rec := doRequest(t, h, tt.method, tt.path, tt.body)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}

			obj := decodeObject(t, rec.Body.Bytes())
			if len(obj) != 1 {
				t.Fatalf("тело ошибки = %v, want ровно одно поле error", keysOf(obj))
			}
			msg, ok := obj["error"].(string)
			if !ok {
				t.Fatalf("поле error = %#v, want string (ключи: %v)", obj["error"], keysOf(obj))
			}
			if msg != tt.wantMsg {
				t.Fatalf("error = %q, want %q", msg, tt.wantMsg)
			}
			if strings.Contains(rec.Body.String(), "password") || strings.Contains(rec.Body.String(), internal) {
				t.Fatalf("внутренняя ошибка утекла наружу: %q", rec.Body.String())
			}
		})
	}
}

// errInternal — вспомогательный конструктор «страшной» внутренней ошибки.
func errInternal(msg string) error { return &internalError{msg: msg} }

type internalError struct{ msg string }

func (e *internalError) Error() string { return e.msg }

// TestRequestBodyDecoding документирует, как разбирается тело запроса:
// что считается валидным JSON, а что — 400 invalid json body.
func TestRequestBodyDecoding(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantMsg    string
		wantText   string
	}{
		{name: "plain object", body: `{"text":"hello"}`, wantStatus: http.StatusCreated, wantText: "hello"},
		{name: "unknown fields are ignored", body: `{"id":"hack","text":"hello","extra":1}`, wantStatus: http.StatusCreated, wantText: "hello"},
		{name: "duplicate key: last one wins", body: `{"text":"a","text":"b"}`, wantStatus: http.StatusCreated, wantText: "b"},
		{name: "null text is empty text", body: `{"text":null}`, wantStatus: http.StatusBadRequest, wantMsg: "text is required"},
		{name: "top-level null is empty text", body: `null`, wantStatus: http.StatusBadRequest, wantMsg: "text is required"},
		{name: "top-level array is invalid", body: `[]`, wantStatus: http.StatusBadRequest, wantMsg: "invalid json body"},
		{name: "top-level string is invalid", body: `"hello"`, wantStatus: http.StatusBadRequest, wantMsg: "invalid json body"},
		{name: "number for text is invalid", body: `{"text":42}`, wantStatus: http.StatusBadRequest, wantMsg: "invalid json body"},
		{name: "object for text is invalid", body: `{"text":{"a":1}}`, wantStatus: http.StatusBadRequest, wantMsg: "invalid json body"},
		{name: "truncated json is invalid", body: `{"text":"a"`, wantStatus: http.StatusBadRequest, wantMsg: "invalid json body"},
		{name: "empty body is invalid", body: ``, wantStatus: http.StatusBadRequest, wantMsg: "invalid json body"},
		{name: "whitespace body is invalid", body: "   \n\t", wantStatus: http.StatusBadRequest, wantMsg: "invalid json body"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newStubRepo()
			h, _ := newTestHandler(repo)

			rec := doRequest(t, h, http.MethodPost, "/notes", tt.body)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if tt.wantMsg != "" {
				if got := decodeError(t, rec.Body); got.Error != tt.wantMsg {
					t.Fatalf("error = %q, want %q", got.Error, tt.wantMsg)
				}
				if len(repo.created) != 0 {
					t.Fatalf("repo.Create вызван %d раз при невалидном теле, want 0", len(repo.created))
				}
				return
			}
			if len(repo.created) != 1 {
				t.Fatalf("repo.Create вызван %d раз, want 1", len(repo.created))
			}
			if repo.created[0].Text != tt.wantText {
				t.Fatalf("сохранён text = %q, want %q", repo.created[0].Text, tt.wantText)
			}
		})
	}
}

// TestDeleteResponseHasNoBody фиксирует контракт 204: пустое тело и
// отсутствие JSON-полезной нагрузки.
func TestDeleteResponseHasNoBody(t *testing.T) {
	repo := newStubRepo()
	repo.notes["id-1"] = note.Note{ID: "id-1", Text: "x", CreatedAt: testTime, UpdatedAt: testTime}
	h, _ := newTestHandler(repo)

	rec := doRequest(t, h, http.MethodDelete, "/notes/id-1", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("body = %q, want пустое тело для 204", rec.Body.String())
	}
	if _, ok := repo.notes["id-1"]; ok {
		t.Fatal("заметка осталась в репозитории после удаления")
	}
}
