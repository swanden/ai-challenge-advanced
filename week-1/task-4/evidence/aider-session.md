
# aider chat started at 2026-07-25 22:33:30

> You can skip this check with --no-gitignore  
> Add .aider* to .gitignore (recommended)? (Y)es/(N)o [Yes]: y  
> Added .aider* to .gitignore  
> /opt/homebrew/bin/aider --model ollama/devstral --no-auto-commits  
> Warning: ollama/devstral expects these environment variables  
> - OLLAMA_API_BASE: Not set  
> You can skip this check with --no-show-model-warnings  
> https://aider.chat/docs/llms/warnings.html  
> Open documentation url for more info? (Y)es/(N)o/(D)on't ask again [Yes]: n  
> Aider v0.86.2  
> Model: ollama/devstral with whole edit format  
> Git repo: .git with 9 files  
> Repo-map: using 4096 tokens, auto refresh  
> https://aider.chat/HISTORY.html#release-notes  
> Would you like to see what's new in this version? (Y)es/(N)o [Yes]: n  

#### Добавь поиск задач: GET /tasks?q=подстрока — фильтрует по названию, регистронезависимо. Пройди по всем слоям, соблюдай конвенции из CLAUDE.md.  
> CLAUDE.md  
> Add file to the chat? (Y)es/(N)o/(D)on't ask again [Yes]: y  

To implement search functionality for tasks based on a query string parameter `q`, we need to make changes in several files. Here's the plan:

1. Update the HTTP handler to handle the new endpoint and extract the query parameter.
2. Add a method to the task store to filter tasks by title containing a substring (case-insensitive).
3. Ensure all layers follow the conventions specified in CLAUDE.md.

Let's start with the changes:

### 1. Update `internal/httpapi/handler.go`

We need to add a new handler function for the search endpoint and update the route registration.

```go
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"log/slog"

	"internal/task"
)

type Handler struct {
	store *task.Store
	log   *slog.Logger
	mux   *http.ServeMux
}

func NewHandler(store *task.Store, log *slog.Logger) *Handler {
	h := &Handler{store: store, log: log, mux: http.NewServeMux()}
	h.mux.HandleFunc("POST /tasks", h.create)
	h.mux.HandleFunc("GET /tasks", h.list)
	h.mux.HandleFunc("GET /tasks/{id}", h.get)
	h.mux.HandleFunc("PATCH /tasks/{id}", h.patch)
	// Add the search route
	h.mux.HandleFunc("GET /tasks?q={query}", h.search)
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

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
			h.writeError(w, r.Context(), http.StatusBadRequest, "title is empty")
		} else {
			h.writeError(w, r.Context(), http.StatusInternalServerError, "failed to create task")
		}
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
		} else {
			h.writeError(w, r.Context(), http.StatusInternalServerError, "failed to get task")
		}
		return
	}

	h.writeJSON(w, r.Context(), http.StatusOK, t)
}

type patchRequest struct {
	Done *bool `json:"done"`
}

func (h *Handler) patch(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		h.writeError(w, r.Context(), http.StatusBadRequest, "id must be a number")
		return
	}

	var req patchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, r.Context(), http.StatusBadRequest, "invalid json body")
		return
	}

	t, err := h.store.SetDone(r.Context(), id, req.Done)
	if err != nil {
		if errors.Is(err, task.ErrNotFound) {
			h.writeError(w, r.Context(), http.StatusNotFound, "task not found")
		} else {
			h.writeError(w, r.Context(), http.StatusInternalServerError, "failed to update task")
		}
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

func (h *Handler) writeJSON(w http.ResponseWriter, ctx context.Context, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		h.log.ErrorContext(ctx, "encode response", slog.Any("error", err))
	}
}

type errorResponse struct {
	Error string `json:"error"`
}

func (h *Handler) writeError(w http.ResponseWriter, ctx context.Context, status int, msg string) {
	h.writeJSON(w, ctx, status, errorResponse{Error: msg})
}

// New search handler function
func (h *Handler) search(w http.ResponseWriter, r *http.Request) {
	query := r.PathValue("query")
	if query == "" {
		h.writeError(w, r.Context(), http.StatusBadRequest, "query parameter is empty")
		return
	}

	tasks, err := h.store.Search(r.Context(), query)
	if err != nil {
		h.writeError(w, r.Context(), http.StatusInternalServerError, "failed to search tasks")
		return
	}
	h.writeJSON(w, r.Context(), http.StatusOK, tasks)
}
```

### 2. Update `internal/task/task.go`

We need to add a new method for searching tasks by title containing a substring.

```go
package task

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

var ErrNotFound = errors.New("task: not found")

var ErrEmptyTitle = errors.New("task: title is empty")

type Task struct {
	ID        int       `json:"id"`
	Title     string    `json:"title"`
	Done      bool      `json:"done"`
	CreatedAt time.Time `json:"created_at"`
}

type Store struct {
	mu     sync.RWMutex
	tasks  map[int]Task
	nextID int
}

func NewStore() *Store {
	return &Store{tasks: make(map[int]Task), nextID: 1}
}

func (s *Store) Create(ctx context.Context, title string) (Task, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return Task{}, ErrEmptyTitle
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	t := Task{
		ID:        s.nextID,
		Title:     title,
		Done:      false,
		CreatedAt: time.Now(),
	}
	s.tasks[s.nextID] = t
	s.nextID++

	return t, nil
}

func (s *Store) Get(ctx context.Context, id int) (Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	t, ok := s.tasks[id]
	if !ok {
		return Task{}, ErrNotFound
	}
	return t, nil
}

func (s *Store) SetDone(ctx context.Context, id int, done bool) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	t, ok := s.tasks[id]
	if !ok {
		return Task{}, ErrNotFound
	}

	t.Done = done

	return t, nil
}

func (s *Store) List(ctx context.Context) ([]Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]Task, 0, len(s.tasks))
	for id := 1; id < s.nextID; id++ {
		if t, ok := s.tasks[id]; ok {
			out = append(out, t)
		}
	}

	return out, nil
}

// New search method for filtering tasks by title containing a substring (case-insensitive)
func (s *Store) Search(ctx context.Context, query string) ([]Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	query = strings.ToLower(query)
	out := make([]Task, 0, len(s.tasks))
	for id := 1; id < s.nextID; id++ {
		if t, ok := s.tasks[id]; ok && strings.Contains(strings.ToLower(t.Title), query) {
			out = append(out, t)
		}
	}

	return out, nil
}
```

### Summary of Changes

1. Added a new route `GET /tasks?q={query}` in the HTTP handler to handle search requests.
2. Implemented a new method `Search` in the task store to filter tasks by title containing a substring (case-insensitive).
3. Ensured all changes follow the conventions specified in CLAUDE.md, including proper error handling and context usage.

These changes should enable searching for tasks based on a query string parameter `q`, filtering by title with case-insensitivity.

> Tokens: 3.2k sent, 1.9k received.  
> internal/httpapi/handler.go  
> Add file to the chat? (Y)es/(N)o/(A)ll/(S)kip all/(D)on't ask again [Yes]: y  
> internal/task/task.go  
> Add file to the chat? (Y)es/(N)o/(A)ll/(S)kip all/(D)on't ask again [Yes]: y  

Let's update both files with the necessary changes to implement the search functionality. Here are the updated versions:

### internal/httpapi/handler.go
````
// Package httpapi — транспортный слой: разбор запроса, вызов домена, формирование ответа.
// Бизнес-логики здесь нет.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"log/slog"

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
	h.mux.HandleFunc("PATCH /tasks/{id}", h.patch)
	// Add the search route
	h.mux.HandleFunc("GET /tasks?q={query}", h.search)
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

// patchRequest — тело запроса на изменение отметки о выполнении.
// Done — указатель, чтобы отличить отсутствующее поле от явного false.
type patchRequest struct {
	Done *bool `json:"done"`
}

func (h *Handler) patch(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		h.writeError(w, r.Context(), http.StatusBadRequest, "id must be a number")
		return
	}

	var req patchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, r.Context(), http.StatusBadRequest, "invalid json body")
		return
	}
	if req.Done == nil {
		h.writeError(w, r.Context(), http.StatusBadRequest, "done is required")
		return
	}

	t, err := h.store.SetDone(r.Context(), id, *req.Done)
	if err != nil {
		if errors.Is(err, task.ErrNotFound) {
			h.writeError(w, r.Context(), http.StatusNotFound, "task not found")
			return
		}
		h.writeError(w, r.Context(), http.StatusInternalServerError, "failed to update task")
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

// New search handler function
func (h *Handler) search(w http.ResponseWriter, r *http.Request) {
	query := r.PathValue("query")
	if query == "" {
		h.writeError(w, r.Context(), http.StatusBadRequest, "query parameter is empty")
		return
	}

	tasks, err := h.store.Search(r.Context(), query)
	if err != nil {
		h.writeError(w, r.Context(), http.StatusInternalServerError, "failed to search tasks")
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
````

### internal/task/task.go
````
// Package task — доменный слой: модель задачи, бизнес-правила и контракт хранилища.
// Про HTTP и про конкретное хранилище этот пакет не знает.
package task

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

// ErrNotFound возвращается, когда задачи с запрошенным id нет.
var ErrNotFound = errors.New("task: not found")

// ErrEmptyTitle возвращается при попытке создать задачу без названия.
var ErrEmptyTitle = errors.New("task: title is empty")

// Task — доменная модель задачи.
type Task struct {
	ID        int       `json:"id"`
	Title     string    `json:"title"`
	Done      bool      `json:"done"`
	CreatedAt time.Time `json:"created_at"`
}

// Store — потокобезопасное хранилище задач в памяти.
type Store struct {
	mu     sync.RWMutex
	tasks  map[int]Task
	nextID int
}

// NewStore создаёт пустое хранилище, готовое к работе.
func NewStore() *Store {
	return &Store{tasks: make(map[int]Task), nextID: 1}
}

// Create проверяет название, присваивает id и сохраняет задачу.
func (s *Store) Create(ctx context.Context, title string) (Task, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return Task{}, ErrEmptyTitle
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	t := Task{
		ID:        s.nextID,
		Title:     title,
		CreatedAt: time.Now().UTC(),
	}
	s.tasks[t.ID] = t
	s.nextID++
	return t, nil
}

// Get возвращает задачу по id или ErrNotFound.
func (s *Store) Get(ctx context.Context, id int) (Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	t, ok := s.tasks[id]
	if !ok {
		return Task{}, ErrNotFound
	}
	return t, nil
}

// SetDone проставляет или снимает отметку о выполнении и возвращает обновлённую задачу.
func (s *Store) SetDone(ctx context.Context, id int, done bool) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	t, ok := s.tasks[id]
	if !ok {
		return Task{}, ErrNotFound
	}

	t.Done = done
	s.tasks[id] = t
	return t, nil
}

// List возвращает все задачи, отсортированные по id для детерминированной выдачи.
func (s *Store) List(ctx context.Context) ([]Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]Task, 0, len(s.tasks))
	for id := 1; id < s.nextID; id++ {
		if t, ok := s.tasks[id]; ok {
			out = append(out, t)
		}
	}
	return out, nil
}

// New search method for filtering tasks by title containing a substring (case-insensitive)
func (s *Store) Search(ctx context.Context, query string) ([]Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	query = strings.ToLower(query)
	out := make([]Task, 0, len(s.tasks))
	for id := 1; id < s.nextID; id++ {
		if t, ok := s.tasks[id]; ok && strings.Contains(strings.ToLower(t.Title), query) {
			out = append(out, t)
		}
	}

	return out, nil
}
````

These changes implement the search functionality for tasks based on a query string parameter `q`, filtering by title with case-insensitivity. The implementation follows the conventions specified in CLAUDE.md, including proper error handling and context usage.

> Tokens: 5.8k sent, 2.2k received.  
> Applied edit to internal/httpapi/handler.go  
> Applied edit to internal/task/task.go  
