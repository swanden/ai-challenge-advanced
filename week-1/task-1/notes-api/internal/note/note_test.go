package note

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeRepo — тестовый дубль Repository, накапливает заметки в мапе.
type fakeRepo struct {
	notes map[string]Note
}

func newFakeRepo() *fakeRepo { return &fakeRepo{notes: make(map[string]Note)} }

func (r *fakeRepo) Create(_ context.Context, n Note) error { r.notes[n.ID] = n; return nil }
func (r *fakeRepo) Get(_ context.Context, id string) (Note, error) {
	n, ok := r.notes[id]
	if !ok {
		return Note{}, ErrNotFound
	}
	return n, nil
}
func (r *fakeRepo) Update(_ context.Context, n Note) error {
	if _, ok := r.notes[n.ID]; !ok {
		return ErrNotFound
	}
	r.notes[n.ID] = n
	return nil
}
func (r *fakeRepo) List(_ context.Context) ([]Note, error) {
	out := make([]Note, 0, len(r.notes))
	for _, n := range r.notes {
		out = append(out, n)
	}
	return out, nil
}

type fixedID struct{ id string }

func (f fixedID) NewID() string { return f.id }

type fixedClock struct{ t time.Time }

func (f fixedClock) Now() time.Time { return f.t }

func TestServiceCreate(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		input   string
		wantErr error
		wantTxt string
	}{
		{name: "trims and stores text", input: "  hello  ", wantErr: nil, wantTxt: "hello"},
		{name: "empty text is rejected", input: "   ", wantErr: ErrEmptyText},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewService(newFakeRepo(), fixedID{id: "abc"}, fixedClock{t: now})

			got, err := svc.Create(context.Background(), tt.input)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Create() err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				return
			}
			if got.Text != tt.wantTxt {
				t.Errorf("Text = %q, want %q", got.Text, tt.wantTxt)
			}
			if !got.CreatedAt.Equal(now) {
				t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, now)
			}
		})
	}
}

func TestServiceUpdate(t *testing.T) {
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	updated := time.Date(2026, 2, 2, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		id      string
		input   string
		wantErr error
		wantTxt string
	}{
		{name: "updates text and bumps UpdatedAt", id: "n1", input: "  new  ", wantErr: nil, wantTxt: "new"},
		{name: "empty text is rejected", id: "n1", input: "   ", wantErr: ErrEmptyText},
		{name: "missing note is not found", id: "nope", input: "x", wantErr: ErrNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeRepo()
			repo.notes["n1"] = Note{ID: "n1", Text: "old", CreatedAt: created, UpdatedAt: created}
			svc := NewService(repo, fixedID{id: "abc"}, fixedClock{t: updated})

			got, err := svc.Update(context.Background(), tt.id, tt.input)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Update() err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				return
			}
			if got.Text != tt.wantTxt {
				t.Errorf("Text = %q, want %q", got.Text, tt.wantTxt)
			}
			if !got.CreatedAt.Equal(created) {
				t.Errorf("CreatedAt = %v, want %v (must be preserved)", got.CreatedAt, created)
			}
			if !got.UpdatedAt.Equal(updated) {
				t.Errorf("UpdatedAt = %v, want %v", got.UpdatedAt, updated)
			}
		})
	}
}
