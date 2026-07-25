package httpapi

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/swanden/ai-challenge-advanced/week-1/task-3/notes-api/internal/note"
)

// TestRoutingPathEdges фиксирует границы карты маршрутов: какие пути ещё
// обслуживает API (и отвечает JSON-ом), а какие проваливаются в файловый
// сервер под "GET /" и отдают его собственный текстовый 404.
func TestRoutingPathEdges(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
		wantJSON   bool
	}{
		{name: "GET /notes/ без id уходит в файловый сервер", method: http.MethodGet, path: "/notes/", wantStatus: http.StatusNotFound},
		{name: "GET /notes/{id}/ с хвостовым слешем не роутится в API", method: http.MethodGet, path: "/notes/id-1/", wantStatus: http.StatusNotFound},
		{name: "GET /notes/{id}/extra не роутится в API", method: http.MethodGet, path: "/notes/id-1/extra", wantStatus: http.StatusNotFound},
		{name: "регистр пути важен", method: http.MethodGet, path: "/NOTES", wantStatus: http.StatusNotFound},
		{name: "query-параметры не влияют на маршрут", method: http.MethodGet, path: "/notes?limit=1", wantStatus: http.StatusOK, wantJSON: true},
		{name: "известный id отдаётся как JSON", method: http.MethodGet, path: "/notes/id-1", wantStatus: http.StatusOK, wantJSON: true},
		{name: "неизвестный id — JSON-ошибка API", method: http.MethodGet, path: "/notes/nope", wantStatus: http.StatusNotFound, wantJSON: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newStubRepo()
			repo.notes["id-1"] = note.Note{ID: "id-1", Text: "x", CreatedAt: testTime, UpdatedAt: testTime}
			h, _ := newTestHandler(repo)

			rec := doRequest(t, h, tt.method, tt.path, "")
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tt.wantStatus, rec.Body.String())
			}

			isJSON := rec.Header().Get("Content-Type") == "application/json"
			if isJSON != tt.wantJSON {
				t.Fatalf("Content-Type = %q, want JSON = %v", rec.Header().Get("Content-Type"), tt.wantJSON)
			}
			if !tt.wantJSON {
				return
			}
			if !json.Valid(rec.Body.Bytes()) {
				t.Fatalf("тело не является валидным JSON: %q", rec.Body.String())
			}
		})
	}
}

// TestMethodNotAllowedAdvertisesAllow проверяет, что на неподдержанный метод
// роутер отдаёт 405 и заголовок Allow с полным набором зарегистрированных
// методов — по нему клиент понимает, что вообще умеет ресурс.
func TestMethodNotAllowedAdvertisesAllow(t *testing.T) {
	tests := []struct {
		name      string
		method    string
		path      string
		wantAllow []string
	}{
		{name: "коллекция", method: http.MethodOptions, path: "/notes", wantAllow: []string{"GET", "HEAD", "POST"}},
		{name: "коллекция: PATCH не поддержан", method: http.MethodPatch, path: "/notes", wantAllow: []string{"GET", "HEAD", "POST"}},
		{name: "элемент", method: http.MethodPut, path: "/notes/id-1", wantAllow: []string{"DELETE", "GET", "HEAD", "PATCH"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _ := newTestHandler(newStubRepo())

			rec := doRequest(t, h, tt.method, tt.path, "")
			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusMethodNotAllowed, rec.Body.String())
			}

			got := splitAllow(rec.Header().Get("Allow"))
			if len(got) != len(tt.wantAllow) {
				t.Fatalf("Allow = %v, want %v", got, tt.wantAllow)
			}
			for i := range got {
				if got[i] != tt.wantAllow[i] {
					t.Fatalf("Allow = %v, want %v", got, tt.wantAllow)
				}
			}
		})
	}
}

func splitAllow(header string) []string {
	out := make([]string, 0, 4)
	for _, part := range strings.Split(header, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// TestPathIDIsUnescaped фиксирует, что id берётся из шаблона маршрута уже
// раскодированным: %2F в пути превращается в "/" и находит заметку с таким id,
// а не ищется буквальная строка "a%2Fb".
func TestPathIDIsUnescaped(t *testing.T) {
	stored := note.Note{ID: "a/b c", Text: "escaped id", CreatedAt: testTime, UpdatedAt: testTime}

	tests := []struct {
		name       string
		path       string
		wantStatus int
	}{
		{name: "экранированный id находится", path: "/notes/a%2Fb%20c", wantStatus: http.StatusOK},
		{name: "буквальный + не считается пробелом", path: "/notes/a%2Fb+c", wantStatus: http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newStubRepo()
			repo.notes[stored.ID] = stored
			h, _ := newTestHandler(repo)

			rec := doRequest(t, h, http.MethodGet, tt.path, "")
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if tt.wantStatus != http.StatusOK {
				return
			}
			var got note.Note
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("unmarshal note %q: %v", rec.Body.String(), err)
			}
			if got != stored {
				t.Fatalf("note = %+v, want %+v", got, stored)
			}
		})
	}
}

// TestDeleteIsIdempotentlyReportedAsNotFound проверяет поведение повторного
// удаления: первый DELETE — 204, второй — 404 с JSON-ошибкой, а не 204.
func TestDeleteIsIdempotentlyReportedAsNotFound(t *testing.T) {
	repo := newStubRepo()
	repo.notes["id-1"] = note.Note{ID: "id-1", Text: "x", CreatedAt: testTime, UpdatedAt: testTime}
	h, _ := newTestHandler(repo)

	if rec := doRequest(t, h, http.MethodDelete, "/notes/id-1", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("первый DELETE status = %d, want %d", rec.Code, http.StatusNoContent)
	}

	rec := doRequest(t, h, http.MethodDelete, "/notes/id-1", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("повторный DELETE status = %d, want %d (body %q)", rec.Code, http.StatusNotFound, rec.Body.String())
	}
	if got := decodeError(t, rec.Body); got.Error != "note not found" {
		t.Errorf("error = %q, want %q", got.Error, "note not found")
	}
}
