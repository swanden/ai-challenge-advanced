package main

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/swanden/ai-challenge-advanced/week-1/task-3/notes-api/internal/httpapi"
	"github.com/swanden/ai-challenge-advanced/week-1/task-3/notes-api/internal/note"
	"github.com/swanden/ai-challenge-advanced/week-1/task-3/notes-api/internal/storage"
)

// Этот файл проверяет сборку всех слоёв вместе (тот же wiring, что в main):
// httpapi.Handler → note.Service → storage.MemoryRepo. Дублей здесь нет,
// кроме Clock/IDGenerator там, где нужен детерминизм.

var wiringStart = time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)

// stepClock — часы, которые сдвигаются на фиксированный шаг при каждом вызове.
// Нужны, чтобы у заметок были заведомо разные и предсказуемые метки времени.
type stepClock struct {
	mu   sync.Mutex
	t    time.Time
	step time.Duration
}

func newStepClock() *stepClock { return &stepClock{t: wiringStart, step: time.Minute} }

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(c.step)
	return c.t
}

// seqID выдаёт предсказуемые идентификаторы id-1, id-2, ...
type seqID struct {
	mu sync.Mutex
	n  int
}

func (g *seqID) NewID() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.n++
	return "id-" + strconv.Itoa(g.n)
}

// newAPI собирает те же зависимости, что и main(), но с подменяемыми
// Clock и IDGenerator.
func newAPI(ids note.IDGenerator, clock note.Clock) http.Handler {
	repo := storage.NewMemoryRepo()
	svc := note.NewService(repo, ids, clock)
	return httpapi.NewHandler(svc, discardLogger())
}

func call(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
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

func decodeNote(t *testing.T, rec *httptest.ResponseRecorder) note.Note {
	t.Helper()
	var n note.Note
	if err := json.Unmarshal(rec.Body.Bytes(), &n); err != nil {
		t.Fatalf("unmarshal note %q: %v", rec.Body.String(), err)
	}
	return n
}

func decodeList(t *testing.T, rec *httptest.ResponseRecorder) []note.Note {
	t.Helper()
	var out []note.Note
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal list %q: %v", rec.Body.String(), err)
	}
	return out
}

// TestWiringNoteLifecycle прогоняет полный жизненный цикл заметки через все
// слои: создание, чтение, обновление, удаление и отражение в списке.
func TestWiringNoteLifecycle(t *testing.T) {
	h := newAPI(&seqID{}, newStepClock())

	created := call(t, h, http.MethodPost, "/notes", `{"text":"  first  "}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("POST status = %d, want %d (body %q)", created.Code, http.StatusCreated, created.Body.String())
	}
	n := decodeNote(t, created)
	if n.ID != "id-1" {
		t.Fatalf("ID = %q, want id-1", n.ID)
	}
	if n.Text != "first" {
		t.Fatalf("Text = %q, want %q — текст должен нормализоваться сервисом", n.Text, "first")
	}
	if !n.CreatedAt.Equal(n.UpdatedAt) {
		t.Fatalf("у новой заметки CreatedAt (%v) != UpdatedAt (%v)", n.CreatedAt, n.UpdatedAt)
	}

	got := decodeNote(t, call(t, h, http.MethodGet, "/notes/"+n.ID, ""))
	if got != n {
		t.Fatalf("GET вернул %+v, want %+v — POST и GET разошлись", got, n)
	}

	listed := decodeList(t, call(t, h, http.MethodGet, "/notes", ""))
	if len(listed) != 1 || listed[0] != n {
		t.Fatalf("список = %+v, want ровно созданную заметку %+v", listed, n)
	}

	patched := call(t, h, http.MethodPatch, "/notes/"+n.ID, `{"text":"  second  "}`)
	if patched.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, want %d (body %q)", patched.Code, http.StatusOK, patched.Body.String())
	}
	upd := decodeNote(t, patched)
	if upd.ID != n.ID {
		t.Fatalf("PATCH сменил ID: %q, want %q", upd.ID, n.ID)
	}
	if upd.Text != "second" {
		t.Fatalf("Text после PATCH = %q, want %q", upd.Text, "second")
	}
	if !upd.CreatedAt.Equal(n.CreatedAt) {
		t.Fatalf("PATCH сдвинул CreatedAt: %v, want %v", upd.CreatedAt, n.CreatedAt)
	}
	if !upd.UpdatedAt.After(n.UpdatedAt) {
		t.Fatalf("PATCH не сдвинул UpdatedAt: %v, было %v", upd.UpdatedAt, n.UpdatedAt)
	}

	// Обновление должно быть видно и через отдельный GET, и в списке.
	if again := decodeNote(t, call(t, h, http.MethodGet, "/notes/"+n.ID, "")); again != upd {
		t.Fatalf("GET после PATCH вернул %+v, want %+v — изменение не доехало до хранилища", again, upd)
	}
	if listed = decodeList(t, call(t, h, http.MethodGet, "/notes", "")); len(listed) != 1 || listed[0] != upd {
		t.Fatalf("список после PATCH = %+v, want %+v", listed, upd)
	}

	deleted := call(t, h, http.MethodDelete, "/notes/"+n.ID, "")
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want %d", deleted.Code, http.StatusNoContent)
	}
	if rec := call(t, h, http.MethodGet, "/notes/"+n.ID, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("GET после DELETE status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if rec := call(t, h, http.MethodDelete, "/notes/"+n.ID, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("повторный DELETE status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if listed = decodeList(t, call(t, h, http.MethodGet, "/notes", "")); len(listed) != 0 {
		t.Fatalf("список после DELETE = %+v, want пустой", listed)
	}
}

// TestWiringListOrder фиксирует сквозной инвариант: список отсортирован
// по времени создания независимо от того, что и в каком порядке меняли.
func TestWiringListOrder(t *testing.T) {
	h := newAPI(&seqID{}, newStepClock())

	texts := []string{"one", "two", "three", "four"}
	ids := make([]string, 0, len(texts))
	for _, text := range texts {
		rec := call(t, h, http.MethodPost, "/notes", `{"text":"`+text+`"}`)
		if rec.Code != http.StatusCreated {
			t.Fatalf("POST %q status = %d, want %d", text, rec.Code, http.StatusCreated)
		}
		ids = append(ids, decodeNote(t, rec).ID)
	}

	// Обновление самой первой заметки не должно поднимать её в конец списка:
	// сортировка идёт по CreatedAt, а не по UpdatedAt.
	if rec := call(t, h, http.MethodPatch, "/notes/"+ids[0], `{"text":"one-updated"}`); rec.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, want %d", rec.Code, http.StatusOK)
	}

	listed := decodeList(t, call(t, h, http.MethodGet, "/notes", ""))
	if len(listed) != len(texts) {
		t.Fatalf("len(list) = %d, want %d", len(listed), len(texts))
	}
	for i, wantID := range ids {
		if listed[i].ID != wantID {
			t.Fatalf("list[%d].ID = %q, want %q (порядок = %v)", i, listed[i].ID, wantID, listIDs(listed))
		}
	}
	for i := 1; i < len(listed); i++ {
		if !listed[i].CreatedAt.After(listed[i-1].CreatedAt) {
			t.Fatalf("список не отсортирован по created_at: %v затем %v", listed[i-1].CreatedAt, listed[i].CreatedAt)
		}
	}

	// Удаление из середины не ломает порядок остальных.
	if rec := call(t, h, http.MethodDelete, "/notes/"+ids[1], ""); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	listed = decodeList(t, call(t, h, http.MethodGet, "/notes", ""))
	wantIDs := []string{ids[0], ids[2], ids[3]}
	if got := listIDs(listed); !equalStrings(got, wantIDs) {
		t.Fatalf("порядок после DELETE = %v, want %v", got, wantIDs)
	}
}

func listIDs(notes []note.Note) []string {
	out := make([]string, 0, len(notes))
	for _, n := range notes {
		out = append(out, n.ID)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestWiringRealIDsAreUnique проверяет боевую связку с note.RandomID:
// каждая заметка получает свой id, ничего не перетирается.
func TestWiringRealIDsAreUnique(t *testing.T) {
	h := newAPI(note.RandomID{}, note.SystemClock{})

	const count = 200
	seen := make(map[string]struct{}, count)
	for i := 0; i < count; i++ {
		rec := call(t, h, http.MethodPost, "/notes", `{"text":"note"}`)
		if rec.Code != http.StatusCreated {
			t.Fatalf("POST #%d status = %d, want %d", i, rec.Code, http.StatusCreated)
		}
		n := decodeNote(t, rec)
		if len(n.ID) != 16 {
			t.Fatalf("len(id) = %d, want 16 (id = %q)", len(n.ID), n.ID)
		}
		if _, err := hex.DecodeString(n.ID); err != nil {
			t.Fatalf("id = %q не hex: %v", n.ID, err)
		}
		if _, dup := seen[n.ID]; dup {
			t.Fatalf("повтор id %q на итерации %d", n.ID, i)
		}
		seen[n.ID] = struct{}{}
		if n.CreatedAt.Location() != time.UTC {
			t.Fatalf("created_at не в UTC: %v", n.CreatedAt)
		}
	}

	listed := decodeList(t, call(t, h, http.MethodGet, "/notes", ""))
	if len(listed) != count {
		t.Fatalf("в списке %d заметок, want %d — часть записей потерялась", len(listed), count)
	}
}

// TestWiringConcurrentRequests гоняет запросы из нескольких горутин по одному
// собранному API: под -race ловит снятие блокировок в хранилище, а проверка
// длины списка — потерянные записи.
func TestWiringConcurrentRequests(t *testing.T) {
	h := newAPI(note.RandomID{}, note.SystemClock{})

	const writers = 32
	ids := make([]string, writers)
	var wg sync.WaitGroup

	wg.Add(writers)
	for i := 0; i < writers; i++ {
		go func(i int) {
			defer wg.Done()
			rec := call(t, h, http.MethodPost, "/notes", `{"text":"concurrent"}`)
			if rec.Code != http.StatusCreated {
				return
			}
			var n note.Note
			if err := json.Unmarshal(rec.Body.Bytes(), &n); err == nil {
				ids[i] = n.ID
			}
		}(i)
	}
	wg.Wait()

	unique := make(map[string]struct{}, writers)
	for i, id := range ids {
		if id == "" {
			t.Fatalf("горутина %d не создала заметку", i)
		}
		unique[id] = struct{}{}
	}
	if len(unique) != writers {
		t.Fatalf("уникальных id = %d, want %d", len(unique), writers)
	}

	listed := decodeList(t, call(t, h, http.MethodGet, "/notes", ""))
	if len(listed) != writers {
		t.Fatalf("в списке %d заметок, want %d — параллельные записи потерялись", len(listed), writers)
	}

	// Параллельные чтения и удаления не должны падать и обязаны в сумме
	// удалить ровно то, что создано.
	wg.Add(writers * 2)
	for i := 0; i < writers; i++ {
		id := ids[i]
		go func() {
			defer wg.Done()
			call(t, h, http.MethodGet, "/notes/"+id, "")
		}()
		go func() {
			defer wg.Done()
			call(t, h, http.MethodDelete, "/notes/"+id, "")
		}()
	}
	wg.Wait()

	listed = decodeList(t, call(t, h, http.MethodGet, "/notes", ""))
	if len(listed) != 0 {
		t.Fatalf("после параллельного удаления осталось %d заметок, want 0", len(listed))
	}
}

// TestWiringValidationIsSharedByCreateAndUpdate проверяет, что правило
// «пустой текст запрещён» одинаково работает на POST и PATCH и что неудачный
// PATCH не портит уже сохранённую заметку.
func TestWiringValidationIsSharedByCreateAndUpdate(t *testing.T) {
	h := newAPI(&seqID{}, newStepClock())

	created := decodeNote(t, call(t, h, http.MethodPost, "/notes", `{"text":"keep me"}`))

	tests := []struct {
		name string
		body string
	}{
		{name: "empty string", body: `{"text":""}`},
		{name: "spaces only", body: `{"text":"    "}`},
		{name: "tabs and newlines only", body: `{"text":"\t\n "}`},
		{name: "missing field", body: `{}`},
		{name: "null text", body: `{"text":null}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if rec := call(t, h, http.MethodPost, "/notes", tt.body); rec.Code != http.StatusBadRequest {
				t.Fatalf("POST status = %d, want %d (body %q)", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			if rec := call(t, h, http.MethodPatch, "/notes/"+created.ID, tt.body); rec.Code != http.StatusBadRequest {
				t.Fatalf("PATCH status = %d, want %d (body %q)", rec.Code, http.StatusBadRequest, rec.Body.String())
			}

			got := decodeNote(t, call(t, h, http.MethodGet, "/notes/"+created.ID, ""))
			if got != created {
				t.Fatalf("заметка изменилась после отклонённого запроса: %+v, want %+v", got, created)
			}
			listed := decodeList(t, call(t, h, http.MethodGet, "/notes", ""))
			if len(listed) != 1 {
				t.Fatalf("в хранилище %d заметок, want 1 — невалидный POST что-то создал", len(listed))
			}
		})
	}
}
