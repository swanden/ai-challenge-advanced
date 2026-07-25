package storage

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/swanden/ai-challenge-advanced/week-1/task-3/notes-api/internal/note"
)

// TestMemoryRepoInstancesAreIndependent фиксирует, что NewMemoryRepo отдаёт
// собственное хранилище, а не общее на весь пакет.
func TestMemoryRepoInstancesAreIndependent(t *testing.T) {
	ctx := context.Background()
	first, second := NewMemoryRepo(), NewMemoryRepo()

	if err := first.Create(ctx, mkNote("id-1", "in first", 0)); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := second.Get(ctx, "id-1"); !errors.Is(err, note.ErrNotFound) {
		t.Fatalf("второй репозиторий видит чужую заметку: err = %v, want %v", err, note.ErrNotFound)
	}
	list, err := second.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("второй репозиторий не пуст: %+v", list)
	}
	if err := second.Update(ctx, mkNote("id-1", "hijack", 0)); !errors.Is(err, note.ErrNotFound) {
		t.Fatalf("Update в чужом репозитории err = %v, want %v", err, note.ErrNotFound)
	}
	if got, err := first.Get(ctx, "id-1"); err != nil || got.Text != "in first" {
		t.Fatalf("первый репозиторий = (%+v, %v), want заметку %q нетронутой", got, err, "in first")
	}
}

// TestMemoryRepoListReturnsSnapshot проверяет, что List отдаёт независимую
// копию: правки в полученном срезе не протекают в хранилище, а последующие
// записи не меняют уже отданный срез.
func TestMemoryRepoListReturnsSnapshot(t *testing.T) {
	ctx := context.Background()
	r := NewMemoryRepo()
	original := mkNote("id-1", "original", 0)
	if err := r.Create(ctx, original); err != nil {
		t.Fatalf("Create: %v", err)
	}

	snapshot, err := r.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(snapshot) != 1 {
		t.Fatalf("len(List()) = %d, want 1", len(snapshot))
	}

	// Правка отданного среза не должна менять хранилище.
	snapshot[0].Text = "mutated by caller"
	stored, err := r.Get(ctx, "id-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored != original {
		t.Fatalf("хранилище = %+v, want %+v — вызывающий смог поменять данные через List", stored, original)
	}

	// Новая запись не должна дописываться в уже отданный срез.
	if err := r.Create(ctx, mkNote("id-2", "second", time.Hour)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(snapshot) != 1 {
		t.Fatalf("len(снимка) = %d, want 1 — List отдал живую ссылку на данные", len(snapshot))
	}
	fresh, err := r.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(fresh) != 2 {
		t.Fatalf("len(List()) = %d, want 2", len(fresh))
	}
}

// TestMemoryRepoGetReturnsSnapshot проверяет, что заметка, отданная из Get,
// не связана с хранилищем: последующий Update не меняет её задним числом.
func TestMemoryRepoGetReturnsSnapshot(t *testing.T) {
	ctx := context.Background()
	r := NewMemoryRepo()
	original := mkNote("id-1", "original", 0)
	if err := r.Create(ctx, original); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := r.Get(ctx, "id-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if err := r.Update(ctx, mkNote("id-1", "updated", time.Hour)); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got != original {
		t.Fatalf("ранее полученная заметка изменилась: %+v, want %+v", got, original)
	}
	after, err := r.Get(ctx, "id-1")
	if err != nil {
		t.Fatalf("Get after Update: %v", err)
	}
	if after.Text != "updated" {
		t.Fatalf("Text = %q, want %q", after.Text, "updated")
	}
}

// TestMemoryRepoDeletedNoteStaysDeleted фиксирует, что после удаления
// заметку нельзя «воскресить» обновлением — только создать заново.
func TestMemoryRepoDeletedNoteStaysDeleted(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name    string
		action  func(r *MemoryRepo) error
		wantErr error
	}{
		{
			name:    "Get после Delete",
			action:  func(r *MemoryRepo) error { _, err := r.Get(ctx, "id-1"); return err },
			wantErr: note.ErrNotFound,
		},
		{
			name:    "Update после Delete",
			action:  func(r *MemoryRepo) error { return r.Update(ctx, mkNote("id-1", "zombie", time.Hour)) },
			wantErr: note.ErrNotFound,
		},
		{
			name:    "Delete после Delete",
			action:  func(r *MemoryRepo) error { return r.Delete(ctx, "id-1") },
			wantErr: note.ErrNotFound,
		},
		{
			name:    "Create после Delete разрешён",
			action:  func(r *MemoryRepo) error { return r.Create(ctx, mkNote("id-1", "reborn", time.Hour)) },
			wantErr: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewMemoryRepo()
			if err := r.Create(ctx, mkNote("id-1", "original", 0)); err != nil {
				t.Fatalf("seed Create: %v", err)
			}
			if err := r.Delete(ctx, "id-1"); err != nil {
				t.Fatalf("Delete: %v", err)
			}

			if err := tt.action(r); !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}

			list, err := r.List(ctx)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			wantLen := 0
			if tt.wantErr == nil {
				wantLen = 1
			}
			if len(list) != wantLen {
				t.Fatalf("в хранилище %d заметок, want %d", len(list), wantLen)
			}
		})
	}
}

// TestMemoryRepoConcurrentUpdateDelete гоняет параллельно запись, обновление,
// удаление и чтение. Под -race тест краснеет, если с Update или Delete снять
// блокировку; итоговая проверка ловит потерянные и «воскресшие» записи.
func TestMemoryRepoConcurrentUpdateDelete(t *testing.T) {
	ctx := context.Background()
	r := NewMemoryRepo()

	const keys = 24
	keyIDs := make([]string, keys)
	for i := range keyIDs {
		keyIDs[i] = "id-" + strconv.Itoa(i)
		if err := r.Create(ctx, mkNote(keyIDs[i], "original", time.Duration(i)*time.Minute)); err != nil {
			t.Fatalf("seed Create: %v", err)
		}
	}

	var wg sync.WaitGroup
	wg.Add(keys * 4)
	for i, id := range keyIDs {
		id, i := id, i
		go func() {
			defer wg.Done()
			_ = r.Update(ctx, mkNote(id, "updated", time.Duration(i)*time.Minute))
		}()
		go func() {
			defer wg.Done()
			_ = r.Delete(ctx, id)
		}()
		go func() {
			defer wg.Done()
			_, _ = r.Get(ctx, id)
		}()
		go func() {
			defer wg.Done()
			_, _ = r.List(ctx)
		}()
	}
	wg.Wait()

	// Все ключи должны быть удалены ровно один раз: Delete у каждого id
	// вызывался единожды и всегда после успешного Create.
	list, err := r.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("после параллельных удалений осталось %d заметок: %v", len(list), ids(list))
	}
	for _, id := range keyIDs {
		if _, err := r.Get(ctx, id); !errors.Is(err, note.ErrNotFound) {
			t.Fatalf("Get(%q) err = %v, want %v", id, err, note.ErrNotFound)
		}
	}
}
