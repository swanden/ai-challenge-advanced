# Уровень 1 — код-тесты (субагент `test-writer`)

Прогон: 2026-07-25 13:39. TASK = `week-1/task-3/notes-api`.

## Покрытие

| пакет | было | стало |
|---|---|---|
| `cmd/notes-api` | 78.9% | **89.5%** |
| `internal/httpapi` | 100.0% | 100.0% |
| `internal/note` | 96.4% | 96.4% |
| `internal/storage` | 100.0% | 100.0% |
| **total (statements)** | **96.4%** | **97.9%** |

Профиль покрытия снят оркестратором независимо: `coverage.out` / `coverage.txt` в этом каталоге.

Пакет уже был покрыт плотно, поэтому основная работа шла не в проценты, а в **поведенческие
дыры при 100% строк**: строки исполнялись, но инвариант никто не проверял (подтверждено
мутацией M9 — существующий тест остался зелёным там, где новый покраснел).

## Что выбрано для покрытия и почему

1. **Бизнес-инварианты домена, невидимые для `fixedClock`.** Существующие тесты используют
   часы-константу, поэтому «время берётся одним снимком» и «id запрашивается на каждую заметку»
   не проверялись: с фиксированными часами `CreatedAt` и `UpdatedAt` совпадают при любой реализации.
2. **Инвариант «клиент не управляет серверными полями».** Кейс «unknown fields are ignored»
   сверял только `text`; что `id`/`created_at`/`updated_at` из тела не протекают в заметку и что
   PATCH бьёт по заметке из пути, а не из тела, — не проверял никто.
3. **Ключ сортировки в `MemoryRepo.List`.** Хелпер `mkNote` проставляет `CreatedAt == UpdatedAt`,
   поэтому тест порядка был слеп к подмене ключа сортировки.
4. **Границы карты маршрутов** — `/notes/`, хвостовой слеш, лишний сегмент, заголовок `Allow`,
   раскодирование `%2F` в id: граница между JSON-API и файловым сервером под `GET /`.
5. **Единственная непокрытая ветка `main()`** — отказ `ListenAndServe`: без `stop()` процесс
   висит навсегда.

## Добавленные тесты

| файл | тесты |
|---|---|
| `internal/note/clock_usage_test.go` | `TestServiceCreateReadsClockOnce`, `TestServiceUpdateTextReadsClockOnce`, `TestServiceDoesNotTouchDepsOnInvalidInput` (3 кейса), `TestServiceCreateAsksIDGeneratorEveryTime` |
| `internal/httpapi/immutable_fields_test.go` | `TestCreateIgnoresClientControlledFields` (3), `TestUpdateIgnoresClientControlledFields` (3), `TestUpdateDoesNotUpsert` |
| `internal/httpapi/routing_edges_test.go` | `TestRoutingPathEdges` (7), `TestMethodNotAllowedAdvertisesAllow` (3), `TestPathIDIsUnescaped` (2), `TestDeleteIsIdempotentlyReportedAsNotFound` |
| `internal/storage/list_order_test.go` | `TestMemoryRepoListSortsByCreatedAtNotUpdatedAt`, `TestMemoryRepoListSortsManyNotes`, `TestMemoryRepoListKeepsAllNotesOnEqualTimestamps`, `TestMemoryRepoListIsStableAcrossCalls` |
| `cmd/notes-api/startup_failure_test.go` | `TestMainExitsWhenPortIsBusy` |

Стиль проекта соблюдён: table-driven, дубли зависимостей (`stepClock`, `countingID`) рядом с
существующими `fixedClock`/`fixedID`, ошибки сверяются через `errors.Is`, статусы — именованными
константами. Всего 207 подтестов, зелено в том числе под `-race`.

**Продуктовый код не тронут** — проверено оркестратором через `git status --porcelain`: пять
новых `_test.go` и ничего больше.

## Мутационная проверка

Каждая мутация вносилась во временный вид, прогонялась точечным `go test -run`, откатывалась
через `git checkout -- <файл>`.

| # | что сломано | какой тест покраснел |
|---|---|---|
| M1 | `note.go:75` `UpdatedAt: now` → `s.clock.Now()` | `TestServiceCreateReadsClockOnce`: «Clock.Now() вызван 2 раз, want 1» + `CreatedAt != UpdatedAt` |
| M2 | `note.go:72` `ID: s.ids.NewID()` → `ID: "note"` | `TestServiceCreateAsksIDGeneratorEveryTime`: обе заметки получили id `"note"` |
| M3 | `note.go:91-99` валидация текста перенесена после `repo.Get` | `TestServiceDoesNotTouchDepsOnInvalidInput`: 2 кейса — репозиторий дёрнут при невалидном вводе, и `ErrNotFound` вместо `ErrEmptyText` |
| M4 | `handler.go:50` в `createRequest` добавлен `ID`, применяется к ответу | `TestCreateIgnoresClientControlledFields`: `ID = "client-id", want "id-1"` |
| M5 | `handler.go:87` в `updateRequest` добавлен `ID`, подменяет id из пути | `TestUpdateIgnoresClientControlledFields`: изменена соседняя заметка `id-2` |
| M6 | `handler.go:74` `r.PathValue("id")` → `TrimPrefix(r.URL.EscapedPath(), "/notes/")` | `TestPathIDIsUnescaped`: 404 вместо 200 на `%2F` |
| M7 | `handler.go:36` маршрут `PATCH /notes/{id}` → `PUT` | `TestMethodNotAllowedAdvertisesAllow/элемент`: 400 вместо 405 |
| M8 | `handler.go:35` `GET /notes/{id}` → `{id...}` | `TestRoutingPathEdges`: 3 кейса — API перехватил `/notes/`, `/notes/{id}/`, `/notes/{id}/extra` |
| M9 | `memory.go:80` ключ сортировки `CreatedAt` → `UpdatedAt` | `TestMemoryRepoListSortsByCreatedAtNotUpdatedAt` — красный. **Существующий `TestMemoryRepoListOrder` при той же мутации остался зелёным**, то есть дыра была настоящая |
| M10 | `memory.go:79-81` сортировка убрана | `TestMemoryRepoListSortsManyNotes` + `TestMemoryRepoListIsStableAcrossCalls` |
| M11 | `main.go:41` убран `stop()` в ветке отказа `ListenAndServe` | `TestMainExitsWhenPortIsBusy`: «main() не завершился за 20s — процесс висит» |
| M12 | `note.go:96` `UpdateText` делает upsert при `ErrNotFound` | `TestUpdateDoesNotUpsert`: 200 вместо 404 |
| M13 | `handler.go:117` DELETE отдаёт 204 вместо 404 на отсутствующей заметке | `TestDeleteIsIdempotentlyReportedAsNotFound` |

13 мутаций — 13 покраснений. После отката всё зелёное.

## Найденный баг (не чинился — профиль `test-writer` правит только `_test.go`)

**`internal/storage/memory.go:79-81` — недетерминированный порядок `List()` при совпадающих `CreatedAt`.**

- **Ожидание:** доккоммент метода обещает сортировку по времени создания, «чтобы выдача была
  детерминированной».
- **Факт:** `sort.Slice` неустойчива, входной срез собирается обходом map (порядок рандомизирован
  Go). При равных `CreatedAt` компаратор всегда возвращает `false` → порядок случайный, **разный
  даже между двумя подряд идущими вызовами `List()` на неизменном хранилище**. Замер: 5 заметок
  с одинаковым `CreatedAt`, 200 вызовов → 5 различных порядков.
- **Достижимость (без завышения):** сквозь HTTP на этой машине воспроизвести не удалось — 20
  конкурентных `POST /notes` дали 20 уникальных `created_at`, порядок стабилен в 30 вызовах
  `GET /notes`. Гранулярность `SystemClock` тут ~1 мкс, полный HTTP-цикл длиннее тика. Риск
  реален на платформах с грубыми часами (Windows, ~0.5–15 мс) и при любом коде, проставляющем
  заметкам одинаковое время.
- **Починка:** тай-брейк по `ID` в компараторе или `sort.SliceStable`.

Тест `TestMemoryRepoListKeepsAllNotesOnEqualTimestamps` намеренно проверяет только состав, а не
порядок при равных метках (иначе был бы флаки), и ссылается на это расхождение в комментарии.

## Что оставлено без тестов и почему

- **`internal/note/deps.go:17-19`, ветка `panic` в `RandomID.NewID`** — в Go 1.26
  `crypto/rand.Read` не возвращает ошибку, ветка недостижима без правки продуктового кода.
- **`cmd/notes-api/main.go:50-52`, ветка `graceful shutdown failed` + `os.Exit(1)`** — требует
  упереться в 10-секундный дедлайн с зависшими соединениями: тест был бы медленным (>10 с) и
  хрупким при ~2 строках склейки. Это и есть остаток 10.5% в `cmd`.
- **`ServeHTTP`, `NewHandler`, `NewService`, `NewMemoryRepo`** — склейка без логики, покрыта
  транзитивно.

## Вердикт Уровня 1

**Зелёный.** Покрытие 96.4% → 97.9%, добавлено 5 файлов тестов, 13/13 мутаций пойманы,
продуктовый код не тронут. Отдельно зафиксирован реальный баг сортировки для профиля bug-fix.
