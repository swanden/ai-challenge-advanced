package storage

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/swanden/ai-challenge-advanced/week-1/task-3/notes-api/internal/note"
)

// TestMemoryRepoListSortsByCreatedAtNotUpdatedAt фиксирует ключ сортировки:
// список упорядочен по времени создания, а не по времени последней правки.
// Заметки заведены так, что порядок по UpdatedAt строго обратный.
func TestMemoryRepoListSortsByCreatedAtNotUpdatedAt(t *testing.T) {
	ctx := context.Background()
	r := NewMemoryRepo()

	const count = 5
	for i := 0; i < count; i++ {
		n := note.Note{
			ID:        "id-" + strconv.Itoa(i),
			Text:      "text",
			CreatedAt: base.Add(time.Duration(i) * time.Hour),
			UpdatedAt: base.Add(time.Duration(count-i) * 24 * time.Hour),
		}
		if err := r.Create(ctx, n); err != nil {
			t.Fatalf("seed Create: %v", err)
		}
	}

	got, err := r.List(ctx)
	if err != nil {
		t.Fatalf("List() err = %v", err)
	}
	want := []string{"id-0", "id-1", "id-2", "id-3", "id-4"}
	if len(got) != len(want) {
		t.Fatalf("List() len = %d, want %d", len(got), len(want))
	}
	for i, wantID := range want {
		if got[i].ID != wantID {
			t.Fatalf("List()[%d].ID = %q, want %q — список отсортирован не по CreatedAt (order = %v)", i, got[i].ID, wantID, ids(got))
		}
	}
}

// TestMemoryRepoListSortsManyNotes проверяет сортировку на выборке, которая
// заведомо не помещается в «маленький» путь сортировки: заметки кладутся
// вперемешку, а на выходе время создания обязано только расти.
func TestMemoryRepoListSortsManyNotes(t *testing.T) {
	ctx := context.Background()
	r := NewMemoryRepo()

	const count = 64
	// Детерминированная перестановка: шаг 17 взаимно прост с 64.
	for i := 0; i < count; i++ {
		idx := (i * 17) % count
		if err := r.Create(ctx, mkNote("id-"+strconv.Itoa(idx), "text", time.Duration(idx)*time.Minute)); err != nil {
			t.Fatalf("seed Create: %v", err)
		}
	}

	got, err := r.List(ctx)
	if err != nil {
		t.Fatalf("List() err = %v", err)
	}
	if len(got) != count {
		t.Fatalf("List() len = %d, want %d", len(got), count)
	}
	for i := 1; i < len(got); i++ {
		if got[i].CreatedAt.Before(got[i-1].CreatedAt) {
			t.Fatalf("List()[%d].CreatedAt (%v) раньше, чем [%d] (%v) — порядок нарушен",
				i, got[i].CreatedAt, i-1, got[i-1].CreatedAt)
		}
	}
	for i := 0; i < count; i++ {
		if got[i].ID != "id-"+strconv.Itoa(i) {
			t.Fatalf("List()[%d].ID = %q, want %q", i, got[i].ID, "id-"+strconv.Itoa(i))
		}
	}
}

// TestMemoryRepoListKeepsAllNotesOnEqualTimestamps проверяет, что сортировка
// ничего не теряет и не дублирует, когда время создания у заметок совпадает.
// Порядок внутри группы с одинаковым CreatedAt репозиторий не гарантирует
// (см. отчёт о нестабильной выдаче), поэтому сверяется состав, а не порядок.
func TestMemoryRepoListKeepsAllNotesOnEqualTimestamps(t *testing.T) {
	ctx := context.Background()
	r := NewMemoryRepo()

	const count = 20
	want := make(map[string]note.Note, count)
	for i := 0; i < count; i++ {
		n := mkNote("id-"+strconv.Itoa(i), "text-"+strconv.Itoa(i), 0)
		want[n.ID] = n
		if err := r.Create(ctx, n); err != nil {
			t.Fatalf("seed Create: %v", err)
		}
	}

	got, err := r.List(ctx)
	if err != nil {
		t.Fatalf("List() err = %v", err)
	}
	if len(got) != count {
		t.Fatalf("List() len = %d, want %d — сортировка потеряла или размножила заметки", len(got), count)
	}

	seen := make(map[string]bool, count)
	for _, n := range got {
		if seen[n.ID] {
			t.Fatalf("заметка %q встречается в списке дважды: %v", n.ID, ids(got))
		}
		seen[n.ID] = true
		if n != want[n.ID] {
			t.Fatalf("заметка %q = %+v, want %+v", n.ID, n, want[n.ID])
		}
	}
	for id := range want {
		if !seen[id] {
			t.Fatalf("заметка %q пропала из списка: %v", id, ids(got))
		}
	}
}

// TestMemoryRepoListIsStableAcrossCalls фиксирует детерминированность выдачи
// при различающихся отметках времени: два подряд идущих List на неизменном
// хранилище обязаны вернуть одинаковый порядок, хотя обход map случаен.
func TestMemoryRepoListIsStableAcrossCalls(t *testing.T) {
	ctx := context.Background()
	r := NewMemoryRepo()

	const count = 16
	for i := 0; i < count; i++ {
		if err := r.Create(ctx, mkNote("id-"+strconv.Itoa(i), "text", time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("seed Create: %v", err)
		}
	}

	first, err := r.List(ctx)
	if err != nil {
		t.Fatalf("List() err = %v", err)
	}
	for call := 0; call < 20; call++ {
		next, err := r.List(ctx)
		if err != nil {
			t.Fatalf("List() err = %v", err)
		}
		if len(next) != len(first) {
			t.Fatalf("List() len = %d, want %d", len(next), len(first))
		}
		for i := range next {
			if next[i] != first[i] {
				t.Fatalf("вызов %d: List()[%d] = %+v, want %+v — выдача недетерминирована", call, i, next[i], first[i])
			}
		}
	}
}
