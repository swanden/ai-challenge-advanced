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

// clientTime — время, которое клиент пытается подсунуть в теле запроса.
var clientTime = time.Date(1999, 12, 31, 23, 59, 59, 0, time.UTC)

// TestCreateIgnoresClientControlledFields фиксирует инвариант: id и обе метки
// времени проставляет сервер (через IDGenerator и Clock), а не клиент. Что бы
// клиент ни прислал в теле, в ответ и в хранилище попадают серверные значения.
func TestCreateIgnoresClientControlledFields(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "клиент шлёт свой id", body: `{"text":"hello","id":"client-id"}`},
		{name: "клиент шлёт свои метки времени", body: `{"text":"hello","created_at":"1999-12-31T23:59:59Z","updated_at":"1999-12-31T23:59:59Z"}`},
		{name: "клиент шлёт всё сразу", body: `{"id":"client-id","text":"hello","created_at":"1999-12-31T23:59:59Z","updated_at":"1999-12-31T23:59:59Z"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newStubRepo()
			h, _ := newTestHandler(repo)

			rec := doRequest(t, h, http.MethodPost, "/notes", tt.body)
			if rec.Code != http.StatusCreated {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusCreated, rec.Body.String())
			}

			var got note.Note
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("unmarshal note %q: %v", rec.Body.String(), err)
			}
			if got.ID != "id-1" {
				t.Errorf("ID = %q, want %q — id должен приходить из IDGenerator, а не из тела запроса", got.ID, "id-1")
			}
			if !got.CreatedAt.Equal(testTime) {
				t.Errorf("CreatedAt = %v, want %v — метку времени проставляет Clock", got.CreatedAt, testTime)
			}
			if !got.UpdatedAt.Equal(testTime) {
				t.Errorf("UpdatedAt = %v, want %v — метку времени проставляет Clock", got.UpdatedAt, testTime)
			}
			if got.CreatedAt.Equal(clientTime) || got.UpdatedAt.Equal(clientTime) {
				t.Errorf("время из тела запроса протекло в заметку: %+v", got)
			}

			if _, ok := repo.notes["client-id"]; ok {
				t.Error("в хранилище появилась заметка с клиентским id")
			}
			if len(repo.created) != 1 {
				t.Fatalf("repo.Create вызван %d раз, want 1", len(repo.created))
			}
			if repo.created[0] != got {
				t.Errorf("сохранено %+v, а отдано %+v — ответ разошёлся с хранилищем", repo.created[0], got)
			}
		})
	}
}

// TestUpdateIgnoresClientControlledFields фиксирует то же правило для PATCH:
// из тела берётся только text. Целевую заметку определяет путь, id и CreatedAt
// не меняются, UpdatedAt приходит из Clock.
func TestUpdateIgnoresClientControlledFields(t *testing.T) {
	createdAt := testTime.Add(-time.Hour)
	target := note.Note{ID: "id-1", Text: "target", CreatedAt: createdAt, UpdatedAt: createdAt}
	other := note.Note{ID: "id-2", Text: "other", CreatedAt: createdAt, UpdatedAt: createdAt}

	tests := []struct {
		name string
		body string
	}{
		{name: "клиент шлёт чужой id", body: `{"text":"updated","id":"id-2"}`},
		{name: "клиент шлёт свои метки времени", body: `{"text":"updated","created_at":"1999-12-31T23:59:59Z","updated_at":"1999-12-31T23:59:59Z"}`},
		{name: "клиент шлёт всё сразу", body: `{"id":"id-2","text":"updated","created_at":"1999-12-31T23:59:59Z","updated_at":"1999-12-31T23:59:59Z"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newStubRepo()
			repo.notes[target.ID] = target
			repo.notes[other.ID] = other
			h, _ := newTestHandler(repo)

			req := httptest.NewRequest(http.MethodPatch, "/notes/id-1", strings.NewReader(tt.body))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusOK, rec.Body.String())
			}

			var got note.Note
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("unmarshal note %q: %v", rec.Body.String(), err)
			}
			if got.ID != target.ID {
				t.Errorf("ID = %q, want %q — целевую заметку задаёт путь, а не тело", got.ID, target.ID)
			}
			if got.Text != "updated" {
				t.Errorf("Text = %q, want %q", got.Text, "updated")
			}
			if !got.CreatedAt.Equal(createdAt) {
				t.Errorf("CreatedAt = %v, want %v — время создания менять нельзя", got.CreatedAt, createdAt)
			}
			if !got.UpdatedAt.Equal(testTime) {
				t.Errorf("UpdatedAt = %v, want %v — метку времени проставляет Clock", got.UpdatedAt, testTime)
			}

			if kept := repo.notes[other.ID]; kept != other {
				t.Errorf("соседняя заметка изменилась: %+v, want %+v", kept, other)
			}
			if stored := repo.notes[target.ID]; stored != got {
				t.Errorf("в хранилище %+v, а отдано %+v", stored, got)
			}
			if len(repo.notes) != 2 {
				t.Errorf("в хранилище %d заметок, want 2 — PATCH создал новую запись", len(repo.notes))
			}
		})
	}
}

// TestUpdateDoesNotUpsert фиксирует, что PATCH не создаёт заметку: неизвестный
// id — это 404 и пустое хранилище, а не «обновление с созданием».
func TestUpdateDoesNotUpsert(t *testing.T) {
	repo := newStubRepo()
	h, _ := newTestHandler(repo)

	req := httptest.NewRequest(http.MethodPatch, "/notes/id-1", strings.NewReader(`{"text":"updated"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusNotFound, rec.Body.String())
	}
	if got := decodeError(t, rec.Body); got.Error != "note not found" {
		t.Errorf("error = %q, want %q", got.Error, "note not found")
	}
	if len(repo.notes) != 0 {
		t.Fatalf("в хранилище %d заметок, want 0 — PATCH создал заметку вместо 404", len(repo.notes))
	}
	if len(repo.created) != 0 {
		t.Fatalf("repo.Create вызван %d раз при PATCH, want 0", len(repo.created))
	}
}
