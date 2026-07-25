package storage

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/swanden/ai-challenge-advanced/week-1/task-3/notes-api/internal/note"
)

var base = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func mkNote(id, text string, offset time.Duration) note.Note {
	return note.Note{ID: id, Text: text, CreatedAt: base.Add(offset), UpdatedAt: base.Add(offset)}
}

// MemoryRepo обязан удовлетворять контракту note.Repository.
var _ note.Repository = (*MemoryRepo)(nil)

func TestMemoryRepoGet(t *testing.T) {
	ctx := context.Background()
	stored := mkNote("id-1", "hello", 0)

	tests := []struct {
		name    string
		seed    []note.Note
		id      string
		wantErr error
		want    note.Note
	}{
		{name: "existing id returns note", seed: []note.Note{stored}, id: "id-1", want: stored},
		{name: "missing id returns ErrNotFound", seed: []note.Note{stored}, id: "id-2", wantErr: note.ErrNotFound},
		{name: "empty repo returns ErrNotFound", id: "id-1", wantErr: note.ErrNotFound},
		{name: "empty id returns ErrNotFound", seed: []note.Note{stored}, id: "", wantErr: note.ErrNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewMemoryRepo()
			for _, n := range tt.seed {
				if err := r.Create(ctx, n); err != nil {
					t.Fatalf("seed Create: %v", err)
				}
			}

			got, err := r.Get(ctx, tt.id)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Get() err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				if got != (note.Note{}) {
					t.Errorf("Get() note = %+v, want zero value on error", got)
				}
				return
			}
			if got != tt.want {
				t.Errorf("Get() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestMemoryRepoUpdate(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name    string
		seed    []note.Note
		update  note.Note
		wantErr error
	}{
		{
			name:   "existing note is overwritten",
			seed:   []note.Note{mkNote("id-1", "old", 0)},
			update: mkNote("id-1", "new", time.Hour),
		},
		{
			name:    "unknown id is ErrNotFound",
			seed:    []note.Note{mkNote("id-1", "old", 0)},
			update:  mkNote("id-2", "new", 0),
			wantErr: note.ErrNotFound,
		},
		{
			name:    "update on empty repo is ErrNotFound",
			update:  mkNote("id-1", "new", 0),
			wantErr: note.ErrNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewMemoryRepo()
			for _, n := range tt.seed {
				if err := r.Create(ctx, n); err != nil {
					t.Fatalf("seed Create: %v", err)
				}
			}

			err := r.Update(ctx, tt.update)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Update() err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				// Неудачный Update не должен создавать заметку «по пути».
				if _, getErr := r.Get(ctx, tt.update.ID); !errors.Is(getErr, note.ErrNotFound) {
					t.Fatalf("failed Update inserted a note: Get err = %v", getErr)
				}
				return
			}

			got, getErr := r.Get(ctx, tt.update.ID)
			if getErr != nil {
				t.Fatalf("Get after Update: %v", getErr)
			}
			if got != tt.update {
				t.Errorf("stored note = %+v, want %+v", got, tt.update)
			}
		})
	}
}

func TestMemoryRepoDelete(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name    string
		seed    []note.Note
		id      string
		wantErr error
	}{
		{name: "existing note is removed", seed: []note.Note{mkNote("id-1", "a", 0)}, id: "id-1"},
		{name: "unknown id is ErrNotFound", seed: []note.Note{mkNote("id-1", "a", 0)}, id: "id-2", wantErr: note.ErrNotFound},
		{name: "delete on empty repo is ErrNotFound", id: "id-1", wantErr: note.ErrNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewMemoryRepo()
			for _, n := range tt.seed {
				if err := r.Create(ctx, n); err != nil {
					t.Fatalf("seed Create: %v", err)
				}
			}

			err := r.Delete(ctx, tt.id)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Delete() err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				// Неудачное удаление не должно трогать остальные заметки.
				list, listErr := r.List(ctx)
				if listErr != nil {
					t.Fatalf("List: %v", listErr)
				}
				if len(list) != len(tt.seed) {
					t.Fatalf("repo size = %d, want %d after failed Delete", len(list), len(tt.seed))
				}
				return
			}

			if _, getErr := r.Get(ctx, tt.id); !errors.Is(getErr, note.ErrNotFound) {
				t.Fatalf("Get after Delete err = %v, want %v", getErr, note.ErrNotFound)
			}
			// Повторное удаление того же id — уже ErrNotFound.
			if again := r.Delete(ctx, tt.id); !errors.Is(again, note.ErrNotFound) {
				t.Fatalf("second Delete err = %v, want %v", again, note.ErrNotFound)
			}
		})
	}
}

// TestMemoryRepoListOrder фиксирует инвариант: выдача отсортирована
// по времени создания, независимо от порядка вставки.
func TestMemoryRepoListOrder(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name    string
		seed    []note.Note
		wantIDs []string
	}{
		{name: "empty repo returns empty slice", wantIDs: []string{}},
		{
			name:    "single note",
			seed:    []note.Note{mkNote("id-1", "a", 0)},
			wantIDs: []string{"id-1"},
		},
		{
			name: "insertion order does not matter",
			seed: []note.Note{
				mkNote("id-c", "c", 3*time.Hour),
				mkNote("id-a", "a", 1*time.Hour),
				mkNote("id-b", "b", 2*time.Hour),
			},
			wantIDs: []string{"id-a", "id-b", "id-c"},
		},
		{
			name: "already sorted stays sorted",
			seed: []note.Note{
				mkNote("id-a", "a", 1*time.Hour),
				mkNote("id-b", "b", 2*time.Hour),
			},
			wantIDs: []string{"id-a", "id-b"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewMemoryRepo()
			for _, n := range tt.seed {
				if err := r.Create(ctx, n); err != nil {
					t.Fatalf("seed Create: %v", err)
				}
			}

			got, err := r.List(ctx)
			if err != nil {
				t.Fatalf("List() err = %v", err)
			}
			if got == nil {
				t.Fatal("List() = nil, want non-nil slice (nil сериализуется в null)")
			}
			if len(got) != len(tt.wantIDs) {
				t.Fatalf("List() len = %d, want %d", len(got), len(tt.wantIDs))
			}
			for i, wantID := range tt.wantIDs {
				if got[i].ID != wantID {
					t.Fatalf("List()[%d].ID = %q, want %q (order = %v)", i, got[i].ID, wantID, ids(got))
				}
			}
		})
	}
}

func ids(notes []note.Note) []string {
	out := make([]string, 0, len(notes))
	for _, n := range notes {
		out = append(out, n.ID)
	}
	return out
}

// TestMemoryRepoCreateOverwrites документирует заявленное поведение:
// повторный Create с тем же id перезаписывает запись и не возвращает ошибку.
func TestMemoryRepoCreateOverwrites(t *testing.T) {
	ctx := context.Background()
	r := NewMemoryRepo()

	if err := r.Create(ctx, mkNote("id-1", "first", 0)); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if err := r.Create(ctx, mkNote("id-1", "second", time.Hour)); err != nil {
		t.Fatalf("second Create: %v", err)
	}

	got, err := r.Get(ctx, "id-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Text != "second" {
		t.Errorf("Text = %q, want %q", got.Text, "second")
	}
	list, err := r.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("List len = %d, want 1", len(list))
	}
}

// TestMemoryRepoConcurrentAccess ловит гонки: при -race снятие блокировок
// в MemoryRepo делает тест красным.
func TestMemoryRepoConcurrentAccess(t *testing.T) {
	ctx := context.Background()
	r := NewMemoryRepo()

	const workers = 16
	var wg sync.WaitGroup
	wg.Add(workers * 3)

	for i := 0; i < workers; i++ {
		id := string(rune('a' + i%26))
		go func() {
			defer wg.Done()
			_ = r.Create(ctx, mkNote(id, "text", time.Duration(i)*time.Second))
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

	list, err := r.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) == 0 {
		t.Fatal("List is empty after concurrent writes")
	}
}
