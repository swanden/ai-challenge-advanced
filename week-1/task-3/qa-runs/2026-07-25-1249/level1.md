# Уровень 1 — код-тесты (субагент `test-writer`)

Прогон: 2026-07-25 12:49, задача `week-1/task-3/notes-api`.

## Покрытие

Профили (копии в этом же каталоге): `cover_before.out` → `cover_after.out`.
Оркестратор сверил профили самостоятельно: файлы различаются, `go tool cover -func` подтверждает
итог **85.7% → 96.4%** по statements.

| пакет | было | стало |
|---|---|---|
| `cmd/notes-api` | 0.0% | **78.9%** |
| `internal/httpapi` | 100.0% | 100.0% |
| `internal/note` | 96.4% | 96.4% |
| `internal/storage` | 100.0% | 100.0% |
| **total (statements)** | **85.7%** | **96.4%** |

Строчное покрытие трёх internal-пакетов уже было близко к 100%, поэтому работа шла не за проценты,
а за поведенческие дыры: проводной контракт JSON, сквозная сборка слоёв и точка входа.
Покрытие `main()` собирается через проброс `-test.gocoverdir` в дочерний процесс.

## Добавленные тесты

| файл | что покрывает |
|---|---|
| `internal/httpapi/contract_test.go` | Проводной контракт API (раньше ответы десериализовались в `note.Note`/`errorResponse`, поэтому переименование json-тега было незаметно): точный набор полей `id/text/created_at/updated_at` и их типы, RFC3339 в UTC (суффикс `Z`), список — всегда JSON-массив (не `null`), тело ошибки — ровно одно поле `error` без утечки внутренностей на всех 10 ветках ошибок, разбор тела запроса (`null`, дубли ключей, top-level массив/строка, обрезанный JSON, неизвестные поля), 204 без тела |
| `cmd/notes-api/wiring_test.go` | Сквозная сборка реальных слоёв `httpapi.Handler → note.Service → storage.MemoryRepo` (раньше каждый слой тестировался только с дублями): полный цикл POST→GET→PATCH→GET→DELETE и его отражение в списке, сортировка списка по `created_at`, боевой `note.RandomID` (200 уникальных 16-hex id), параллельные POST/GET/DELETE под `-race`, общее правило валидации пустого текста для POST и PATCH |
| `cmd/notes-api/main_test.go` | Точка входа, ранее 0%: `main()` запускается дочерним процессом (self re-exec), сервис реально слушает адрес, отдаёт API и встроенный UI, по SIGTERM завершается с кодом 0, пишет `server starting`/`shutting down`, освобождает порт. Пропускается при занятом порте или `-short` |
| `internal/storage/memory_invariants_test.go` | Инварианты хранилища: независимость экземпляров `NewMemoryRepo`, `List`/`Get` отдают снимок (правки вызывающего не протекают), удалённую заметку нельзя воскресить через `Update`, параллельные `Update`/`Delete`/`Get`/`List` |

## Мутационная проверка

Каждая мутация вносилась в продовый файл, прогонялись только новые тесты, затем `git checkout` файла.

| мутация | тест покраснел |
|---|---|
| `internal/note/note.go:24` json-тег `text` → `body` | ДА — `TestNoteJSONContract`, `TestListJSONContract` |
| `internal/httpapi/handler.go:146` json-тег `error` → `message` | ДА — `TestErrorJSONContract` (10 подтестов) |
| `internal/httpapi/handler.go:64` пустой текст на POST → 409 вместо 400 | ДА — `TestErrorJSONContract/create_empty_text`, `TestRequestBodyDecoding` |
| `internal/httpapi/handler.go:123` DELETE отвечает 200 с телом вместо 204 | ДА — `TestDeleteResponseHasNoBody` |
| `internal/note/note.go:65` `Create` без `TrimSpace` | ДА — `TestWiringNoteLifecycle`, `TestWiringValidationIsSharedByCreateAndUpdate` |
| `internal/note/note.go:102` `UpdateText` не двигает `UpdatedAt` | ДА — `TestWiringNoteLifecycle` |
| `internal/note/note.go:101` `UpdateText` перетирает `CreatedAt` | ДА — `TestWiringNoteLifecycle`, `TestWiringListOrder` |
| `internal/storage/memory.go:80` сортировка `List`: `Before` → `After` | ДА — `TestWiringListOrder` |
| `internal/storage/memory.go:22` общий map на все `NewMemoryRepo` | ДА — `TestMemoryRepoInstancesAreIndependent`, `TestMemoryRepoDeletedNoteStaysDeleted` |
| `internal/storage/memory.go:51` `Update` превращён в upsert | ДА — `TestMemoryRepoDeletedNoteStaysDeleted`, `TestMemoryRepoConcurrentUpdateDelete` |
| `internal/storage/memory.go:60` `Delete` без `mu.Lock()` | ДА — `TestMemoryRepoConcurrentUpdateDelete` под `-race` (DATA RACE) |
| `cmd/notes-api/main.go:28` `Addr: ":8080"` → `":8081"` | ДА — `TestMainServesAndShutsDownGracefully` |
| `cmd/notes-api/main.go:34` SIGTERM убран из `signal.NotifyContext` | ДА — `TestMainServesAndShutsDownGracefully` (код выхода ≠ 0) |
| `cmd/notes-api/main.go:48` таймаут shutdown `10s` → `0s` | **НЕТ — эквивалентная мутация.** `Shutdown` закрывает слушателей и idle-соединения и возвращает `nil` до проверки дедлайна, если активных запросов нет |

**13 из 14 мутаций пойманы**, 14-я обоснована как эквивалентная.

## Наблюдения (не баги, тестов не писал)

- `internal/storage/memory.go:79` — `sort.Slice` неустойчив, а комментарий обещает детерминированную
  выдачу. При одинаковом `CreatedAt` порядок двух заметок не определён. Лечится `sort.SliceStable`
  с тай-брейком по `ID`.
- `internal/httpapi/handler.go:56` — `json.Decoder.Decode` читает только первое значение, поэтому
  `{"text":"a"} мусор` принимается с 201. Текущее поведение зафиксировано тестом как есть.

## Оставлено без тестов

- `internal/note/deps.go:17-19` — ветка `panic` в `RandomID.NewID` недостижима, точки подмены нет.
- `cmd/notes-api/main.go:39-42` и `50-53` — ошибки `ListenAndServe`/`Shutdown`; первая даёт флаки-гонку
  с самим тестом, вторая снаружи не провоцируется.
- Дренаж in-flight запросов при graceful shutdown — в API нет долгого эндпоинта.

## Проверка целостности (оркестратором)

`git status --short` после Уровня 1 — изменены **только** новые тест-файлы, продовый код не тронут:

```
?? week-1/task-3/notes-api/cmd/notes-api/main_test.go
?? week-1/task-3/notes-api/cmd/notes-api/wiring_test.go
?? week-1/task-3/notes-api/internal/httpapi/contract_test.go
?? week-1/task-3/notes-api/internal/storage/memory_invariants_test.go
```
