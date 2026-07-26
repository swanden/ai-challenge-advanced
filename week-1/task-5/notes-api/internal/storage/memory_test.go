package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/swanden/ai-challenge-advanced/week-1/task-5/notes-api/internal/note"
)

func TestMemoryRepoCreateGet(t *testing.T) {
	repo := NewMemoryRepo()
	ctx := context.Background()
	n := note.Note{ID: "abc", Text: "hello", CreatedAt: time.Now().UTC()}

	if err := repo.Create(ctx, n); err != nil {
		t.Fatalf("Create() err = %v, want nil", err)
	}

	got, err := repo.Get(ctx, "abc")
	if err != nil {
		t.Fatalf("Get() err = %v, want nil", err)
	}
	if got != n {
		t.Errorf("Get() = %+v, want %+v", got, n)
	}
}

func TestMemoryRepoGetNotFound(t *testing.T) {
	repo := NewMemoryRepo()

	_, err := repo.Get(context.Background(), "missing")
	if !errors.Is(err, note.ErrNotFound) {
		t.Fatalf("Get() err = %v, want %v", err, note.ErrNotFound)
	}
}

func TestMemoryRepoUpdate(t *testing.T) {
	tests := []struct {
		name    string
		seed    bool
		wantErr error
	}{
		{name: "existing note is updated", seed: true, wantErr: nil},
		{name: "unknown note is rejected", seed: false, wantErr: note.ErrNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := NewMemoryRepo()
			ctx := context.Background()
			n := note.Note{ID: "abc", Text: "hello"}
			if tt.seed {
				if err := repo.Create(ctx, n); err != nil {
					t.Fatalf("Create() err = %v, want nil", err)
				}
			}

			n.Text = "updated"
			err := repo.Update(ctx, n)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Update() err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				return
			}

			got, err := repo.Get(ctx, n.ID)
			if err != nil {
				t.Fatalf("Get() err = %v, want nil", err)
			}
			if got.Text != "updated" {
				t.Errorf("Text = %q, want %q", got.Text, "updated")
			}
		})
	}
}

func TestMemoryRepoDelete(t *testing.T) {
	tests := []struct {
		name    string
		seed    bool
		wantErr error
	}{
		{name: "existing note is deleted", seed: true, wantErr: nil},
		{name: "unknown note is rejected", seed: false, wantErr: note.ErrNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := NewMemoryRepo()
			ctx := context.Background()
			if tt.seed {
				if err := repo.Create(ctx, note.Note{ID: "abc"}); err != nil {
					t.Fatalf("Create() err = %v, want nil", err)
				}
			}

			err := repo.Delete(ctx, "abc")
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Delete() err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				return
			}

			if _, err := repo.Get(ctx, "abc"); !errors.Is(err, note.ErrNotFound) {
				t.Errorf("Get() after Delete() err = %v, want %v", err, note.ErrNotFound)
			}
		})
	}
}

func TestMemoryRepoListEmpty(t *testing.T) {
	repo := NewMemoryRepo()

	got, err := repo.List(context.Background())
	if err != nil {
		t.Fatalf("List() err = %v, want nil", err)
	}
	if got == nil {
		t.Fatal("List() = nil, want non-nil empty slice")
	}
	if len(got) != 0 {
		t.Errorf("List() len = %d, want 0", len(got))
	}
}

func TestMemoryRepoList(t *testing.T) {
	repo := NewMemoryRepo()
	ctx := context.Background()

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	third := note.Note{ID: "third", CreatedAt: base.Add(2 * time.Hour)}
	first := note.Note{ID: "first", CreatedAt: base}
	second := note.Note{ID: "second", CreatedAt: base.Add(time.Hour)}

	for _, n := range []note.Note{third, first, second} {
		if err := repo.Create(ctx, n); err != nil {
			t.Fatalf("Create() err = %v, want nil", err)
		}
	}

	got, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List() err = %v, want nil", err)
	}
	want := []string{"first", "second", "third"}
	if len(got) != len(want) {
		t.Fatalf("List() len = %d, want %d", len(got), len(want))
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Errorf("List()[%d].ID = %q, want %q", i, got[i].ID, id)
		}
	}
}
