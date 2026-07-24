# CLAUDE.md — notes-api

Небольшой HTTP-сервис заметок на Go. Демонстрирует чистую слоёную архитектуру
на стандартной библиотеке. Этот файл описывает, как в проекте принято писать код,
чтобы новый код было не отличить от существующего.

## Стек и зависимости

Язык — Go. Код живёт в монорепозитории `github.com/swanden/ai-challenge-advanced`,
это задание — в каталоге `week-1/task-1/notes-api/`. Внутренние пакеты импортируются
полным путём модуля. Только стандартная библиотека: `net/http` для
транспорта, `log/slog` для логов, `encoding/json` для сериализации. Внешних
зависимостей нет и добавлять их без явной необходимости нельзя. HTTP-роутинг —
на `http.ServeMux` с method-шаблонами (`"PATCH /notes/{id}"`), без сторонних
роутеров.

## Архитектура (слои и направление зависимостей)

Код разбит на слои, и зависимости направлены только внутрь, к домену:

- `cmd/notes-api` — точка входа. Только сборка зависимостей (wiring), запуск
  сервера и graceful shutdown. Никакой бизнес-логики.
- `internal/note` — доменный слой. Модель `Note`, бизнес-правила в `Service`,
  интерфейсы зависимостей (`Repository`, `IDGenerator`, `Clock`). Этот пакет
  НЕ импортирует `httpapi` и `storage` и ничего не знает про HTTP и про то,
  где данные лежат физически.
- `internal/storage` — реализации `note.Repository` (сейчас `MemoryRepo`).
- `internal/httpapi` — транспорт. Разбор запроса, вызов `note.Service`,
  формирование ответа. Бизнес-логики здесь быть не должно.

Правило потока: HTTP-хендлер → `note.Service` → `note.Repository`. Хендлер
никогда не ходит в репозиторий напрямую и не реализует бизнес-правила сам.

## Naming conventions

Конструкторы называются `NewXxx` и возвращают `*Xxx` (`NewService`,
`NewMemoryRepo`, `NewHandler`). Доменные ошибки — переменные уровня пакета с
префиксом `Err` (`ErrNotFound`, `ErrEmptyText`). Экспортируются только те
идентификаторы, что нужны снаружи пакета; всё остальное — со строчной буквы.
Файлы называются по смыслу (`note.go`, `memory.go`, `handler.go`), тесты — рядом
с кодом в `_test.go`.

## Обязательные паттерны

**`context.Context` — первым аргументом каждого метода, который делает I/O или
может быть отменён.** Он прокладывается сквозь все слои: хендлер берёт
`r.Context()` и передаёт в сервис, сервис — в репозиторий. Свой контекст внутри
метода (`context.Background()`, `context.TODO()`) создавать нельзя — это обрывает
отмену и дедлайны.

**Время и идентификаторы — только через абстракции `Clock` и `IDGenerator`.**
В коде домена нельзя звать `time.Now()` или генерировать id напрямую — иначе
логику не зафиксировать в тестах. Берём `s.clock.Now()` и `s.ids.NewID()`.
Время — всегда UTC (`SystemClock.Now()` уже возвращает UTC).

**Валидация входных данных — в сервисе, а не в хендлере.** `Service` сам
делает `TrimSpace`, проверяет пустоту и возвращает доменную ошибку. Хендлер
только транслирует эту ошибку в HTTP-статус.

**Тело запроса — типизированная структура, а не `map[string]any`.** Для каждого
эндпоинта заводим `xxxRequest struct` с json-тегами. Type assertion из
`map[string]interface{}` запрещён.

**Ошибки различаем через `errors.Is` и мапим на статусы явно.** В хендлере:
`errors.Is(err, note.ErrNotFound)` → 404, `errors.Is(err, note.ErrEmptyText)`
→ 400, иначе → 500. Наружу отдаём безопасное сообщение в формате
`errorResponse{Error: ...}`, а не `err.Error()`.

**HTTP-статусы — только именованными константами** (`http.StatusNotFound`),
никогда числом.

**Ошибки не проглатываем.** Ошибку кодирования ответа логируем через `slog`
с контекстом (`h.log.ErrorContext(ctx, ...)`).

## Хороший код (эталоны из проекта)

Метод сервиса: `ctx` первым, зависимости — через абстракции, доменная ошибка
на невалидный вход.

```go
func (s *Service) Create(ctx context.Context, text string) (Note, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Note{}, ErrEmptyText
	}
	now := s.clock.Now()
	n := Note{ID: s.ids.NewID(), Text: text, CreatedAt: now, UpdatedAt: now}
	if err := s.repo.Create(ctx, n); err != nil {
		return Note{}, err
	}
	return n, nil
}
```

Хендлер: типизированный запрос, `r.Context()` в сервис, различение ошибок,
единый формат ответа.

```go
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
```

Метод репозитория: `ctx` первым, под блокировкой, доменная ошибка при отсутствии.

```go
func (r *MemoryRepo) Update(ctx context.Context, n note.Note) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.notes[n.ID]; !ok {
		return note.ErrNotFound
	}
	r.notes[n.ID] = n
	return nil
}
```

Тест: table-driven, зависимости подменяются дублями (`fixedID`, `fixedClock`),
ошибки сверяются через `errors.Is`.

## Антипаттерны (так писать нельзя)

- Метод, делающий I/O, **без `context.Context`** первым аргументом; либо
  `context.Background()` / `context.TODO()`, созданный внутри метода.
- **`time.Now()` или генерация id напрямую** в доменном коде мимо `Clock` /
  `IDGenerator`.
- **`map[string]interface{}` для тела запроса** и `req["text"].(string)` без
  проверки `ok` — это паника на неожиданном типе.
- **Магические числа статусов** (`400`, `404`) вместо `http.StatusXxx`.
- **`http.Error(w, err.Error(), ...)`** — утечка внутренней ошибки наружу и
  потеря единого формата ответа; плюс привязка любой ошибки к одному статусу
  вместо различения через `errors.Is`.
- **Проглоченная ошибка** (`json.NewEncoder(w).Encode(v)` без проверки/лога).
- **Бизнес-логика или валидация в хендлере**; хендлер, ходящий в репозиторий
  мимо сервиса.
- Внешние зависимости и сторонние роутеры без необходимости.

## Шаблон типичного файла

```go
// Package <name> — одно-два предложения о назначении пакета и его границах.
package <name>

import (
	"context"          // стандартная библиотека
	"errors"

	"github.com/swanden/ai-challenge-advanced/week-1/task-1/notes-api/internal/<other>" // внутренние пакеты — отдельной группой
)

// Xxx — что это и зачем. Экспортируемые типы документируем.
type Xxx struct {
	dep Dependency
}

// NewXxx собирает Xxx из зависимостей.
func NewXxx(dep Dependency) *Xxx {
	return &Xxx{dep: dep}
}

// Do — что делает метод. ctx первым аргументом.
func (x *Xxx) Do(ctx context.Context, in Input) (Output, error) {
	// валидация → работа → возврат доменной ошибки при необходимости
}
```

## Проверка перед сдачей

Любой новый код должен пройти проверки, запущенные ИЗ КОРНЯ репозитория:
`go build ./week-1/task-1/notes-api/...`, `go vet ./week-1/task-1/notes-api/...`,
`gofmt -l week-1` (пустой вывод) и `go test ./week-1/task-1/notes-api/...`.
