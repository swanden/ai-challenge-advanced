# Уровень 1 — код-тесты (субагент `test-writer`)

## Покрытие

| пакет | было | стало |
|---|---|---|
| `internal/httpapi` | 0.0% | **100.0%** |
| `internal/note` | 47.1% | **94.1%** |
| `internal/storage` | 0.0% | **100.0%** |
| `cmd/notes-api` | 0.0% | 0.0% (только wiring, не покрывался) |
| **итого по модулю** | **7.0%** | **82.6%** |

Непокрытой осталась одна строка в `internal/note` — panic-ветка `RandomID.NewID` при отказе
`crypto/rand`, недостижима без подмены `rand.Reader`.

Профили прогона: `coverage.out`, `coverage.html` (в этом же каталоге).

## Добавленные файлы тестов

| файл | что покрывает |
|---|---|
| `internal/httpapi/handler_test.go` | 4 эндпоинта; маппинг `ErrEmptyText`→400, `ErrNotFound`→404, прочее→500; битый/пустой JSON и неверный тип поля → 400; 201 на создании, 204 без тела на удалении; `Content-Type: application/json`; отсутствие утечки внутренней ошибки в тело; `[]` вместо `null` в списке; проброс `r.Context()` до репозитория; карта маршрутов (405 на PUT/PATCH/POST, отдача `index.html`, 404 на неизвестную статику); лог ошибки кодирования через `slog` |
| `internal/note/service_test.go` | нормализация текста (trim, unicode, внутренние пробелы), отказ на пустом вводе без похода в репозиторий; id и время только из `IDGenerator`/`Clock`, `CreatedAt == UpdatedAt`; проброс ошибки репозитория и нулевая `Note` при ней; делегирование `Get`/`Delete`/`List`; проброс `ctx` и отменённый контекст |
| `internal/storage/memory_test.go` | `ErrNotFound` через `errors.Is` для Get/Update/Delete; отсутствие побочных эффектов у неудачных Update/Delete; идемпотентность повторного Delete; перезапись Create с тем же id; сортировка `List` по `CreatedAt`; non-nil пустой срез; соответствие интерфейсу `note.Repository`; конкурентный доступ под `-race` |
| `internal/note/deps_test.go` | `RandomID`: формат (16 hex) и уникальность на 1000 генераций; `SystemClock`: строго UTC, попадание между двумя `time.Now()`, монотонность |

Продуктовый код не изменён — `git diff` по задаче пуст, в статусе только 4 новых `*_test.go`
(проверено оркестратором отдельно).

## Мутационная проверка — 23 из 23 убиты

| мутация | тест покраснел |
|---|---|
| `handler.go:56` 400→500 на битом JSON | `TestHandlerCreate/broken_json_is_400` и др. |
| `handler.go:62` `ErrEmptyText`→`ErrNotFound` | `TestHandlerCreate/blank_text_is_400_from_domain` |
| `handler.go:77` 404→500 для ненайденной | `TestHandlerGet/unknown_id_is_404` |
| `handler.go:74` `r.Context()`→`context.Background()` | `TestHandlerPassesRequestContext/get` |
| `handler.go:96` 204→200 при удалении | `TestHandlerDelete/existing_note_is_204_without_body` |
| `handler.go:112` проглочена ошибка кодирования | `TestWriteJSONLogsEncodeError` |
| `handler.go:110` убран `Content-Type` | `TestHandlerCreate` (все подтесты) |
| `handler.go:36` маршрут `DELETE`→`PUT` | `TestHandlerRouting/DELETE_/notes/{id}_is_routed` |
| `handler.go:101` убрана ветка ошибки в `list` | `TestHandlerList/repository_failure_is_500` |
| `note.go:65` убран `strings.TrimSpace` | `TestServiceCreateNormalization` (4 подтеста) |
| `note.go:66` отключена проверка пустоты | `TestServiceCreateNormalization/empty_string_is_rejected` |
| `note.go:75` `UpdatedAt` сдвинут на секунду | `TestServiceCreateUsesAbstractions` |
| `note.go:78` ошибка репозитория проглочена | `TestServiceCreateRepoError` |
| `note.go:85` `Get` с `context.Background()` | `TestServicePassesContext/Get` |
| `note.go:90` `Delete` проглатывает ошибку | `TestServicePassThrough/Delete_propagates_repo_error` |
| `deps.go:16` `make([]byte, 8)`→`4` | `TestRandomIDNewID/single_id` |
| `deps.go:27` `time.Now().UTC()`→`time.Now()` | `TestSystemClockNow` |
| `memory.go:42` `Get` возвращает `nil` вместо `ErrNotFound` | `TestMemoryRepoGet` (3 подтеста) |
| `memory.go:51` убрана проверка в `Update` | `TestMemoryRepoUpdate/unknown_id_is_ErrNotFound` |
| `memory.go:62` убрана проверка в `Delete` | `TestMemoryRepoDelete` (3 подтеста) |
| `memory.go:80` сортировка `Before`→`After` | `TestMemoryRepoListOrder/insertion_order_does_not_matter` |
| `memory.go:75` `make(..., 0, n)`→`var out []note.Note` | `TestMemoryRepoListOrder/empty_repo_returns_empty_slice` |
| `memory.go:30` убраны `mu.Lock()/Unlock()` в `Create` | `TestMemoryRepoConcurrentAccess` под `-race` (DATA RACE) |

Первая версия `TestHandlerRouting` мутацию маршрута не ловила — тест был усилен.

## Замечание по расхождению кода и документации

`CLAUDE.md` приводит `"PATCH /notes/{id}"` как пример method-шаблона, `note.Repository` и
`storage.MemoryRepo` реализуют `Update`, но ни `note.Service`, ни `httpapi.Handler` метода
обновления не имеют — `PATCH /notes/{id}` отдаёт 405. Текущее поведение зафиксировано тестом
`TestHandlerRouting/PATCH_/notes/{id}_is_not_routed`. Багом рантайма это не является.

## Вердикт Уровня 1: зелено
