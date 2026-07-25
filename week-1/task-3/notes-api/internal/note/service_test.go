package note

import (
	"context"
	"errors"
	"testing"
	"time"
)

// recordingRepo — дубль Repository, который запоминает переданный ctx и
// аргументы, а также умеет возвращать заранее заданную ошибку.
type recordingRepo struct {
	inner   *fakeRepo
	err     error
	gotCtx  context.Context
	gotNote Note
	gotID   string
	calls   int
}

func newRecordingRepo() *recordingRepo { return &recordingRepo{inner: newFakeRepo()} }

func (r *recordingRepo) Create(ctx context.Context, n Note) error {
	r.gotCtx, r.gotNote, r.calls = ctx, n, r.calls+1
	if r.err != nil {
		return r.err
	}
	return r.inner.Create(ctx, n)
}

func (r *recordingRepo) Get(ctx context.Context, id string) (Note, error) {
	r.gotCtx, r.gotID, r.calls = ctx, id, r.calls+1
	if r.err != nil {
		return Note{}, r.err
	}
	return r.inner.Get(ctx, id)
}

func (r *recordingRepo) Update(ctx context.Context, n Note) error {
	r.gotCtx, r.gotNote, r.calls = ctx, n, r.calls+1
	if r.err != nil {
		return r.err
	}
	return r.inner.Update(ctx, n)
}

func (r *recordingRepo) Delete(ctx context.Context, id string) error {
	r.gotCtx, r.gotID, r.calls = ctx, id, r.calls+1
	if r.err != nil {
		return r.err
	}
	return r.inner.Delete(ctx, id)
}

func (r *recordingRepo) List(ctx context.Context) ([]Note, error) {
	r.gotCtx, r.calls = ctx, r.calls+1
	if r.err != nil {
		return nil, r.err
	}
	return r.inner.List(ctx)
}

var svcTime = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

func newTestService(repo Repository) *Service {
	return NewService(repo, fixedID{id: "id-1"}, fixedClock{t: svcTime})
}

// TestServiceCreateNormalization покрывает граничные варианты текста:
// сервис обязан сам делать TrimSpace и отбивать пустой ввод.
func TestServiceCreateNormalization(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantErr  error
		wantText string
	}{
		{name: "plain text is kept as is", input: "hello", wantText: "hello"},
		{name: "surrounding spaces are trimmed", input: "   hello   ", wantText: "hello"},
		{name: "newlines and tabs are trimmed", input: "\n\t hello \t\n", wantText: "hello"},
		{name: "inner spaces are preserved", input: "  a  b  ", wantText: "a  b"},
		{name: "unicode text survives", input: " заметка ", wantText: "заметка"},
		{name: "empty string is rejected", input: "", wantErr: ErrEmptyText},
		{name: "spaces only are rejected", input: "     ", wantErr: ErrEmptyText},
		{name: "tabs and newlines only are rejected", input: "\t\n\r ", wantErr: ErrEmptyText},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newRecordingRepo()
			svc := newTestService(repo)

			got, err := svc.Create(context.Background(), tt.input)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Create() err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				if got != (Note{}) {
					t.Errorf("Create() note = %+v, want zero value on error", got)
				}
				if repo.calls != 0 {
					t.Errorf("repo called %d times on invalid input, want 0", repo.calls)
				}
				return
			}

			if got.Text != tt.wantText {
				t.Errorf("Text = %q, want %q", got.Text, tt.wantText)
			}
			if repo.gotNote.Text != tt.wantText {
				t.Errorf("stored Text = %q, want %q (сервис сохранил ненормализованный текст)", repo.gotNote.Text, tt.wantText)
			}
		})
	}
}

// TestServiceCreateUsesAbstractions фиксирует правило: id и время берутся
// только из IDGenerator и Clock, а CreatedAt == UpdatedAt у новой заметки.
func TestServiceCreateUsesAbstractions(t *testing.T) {
	repo := newRecordingRepo()
	svc := newTestService(repo)

	got, err := svc.Create(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Create() err = %v", err)
	}

	if got.ID != "id-1" {
		t.Errorf("ID = %q, want %q (id должен приходить из IDGenerator)", got.ID, "id-1")
	}
	if !got.CreatedAt.Equal(svcTime) {
		t.Errorf("CreatedAt = %v, want %v (время должно приходить из Clock)", got.CreatedAt, svcTime)
	}
	if !got.UpdatedAt.Equal(svcTime) {
		t.Errorf("UpdatedAt = %v, want %v", got.UpdatedAt, svcTime)
	}
	if !got.CreatedAt.Equal(got.UpdatedAt) {
		t.Errorf("CreatedAt (%v) != UpdatedAt (%v) у новой заметки", got.CreatedAt, got.UpdatedAt)
	}
	if repo.gotNote != got {
		t.Errorf("repo got %+v, want %+v — сервис вернул не то, что сохранил", repo.gotNote, got)
	}
}

// TestServiceCreateRepoError проверяет, что ошибка хранилища доходит наверх
// как есть и не превращается в частично заполненную заметку.
func TestServiceCreateRepoError(t *testing.T) {
	repoErr := errors.New("storage unavailable")
	repo := newRecordingRepo()
	repo.err = repoErr
	svc := newTestService(repo)

	got, err := svc.Create(context.Background(), "hello")
	if !errors.Is(err, repoErr) {
		t.Fatalf("Create() err = %v, want %v", err, repoErr)
	}
	if got != (Note{}) {
		t.Errorf("Create() note = %+v, want zero value on repo error", got)
	}
}

// TestServiceUpdateTextNormalization покрывает валидацию текста в UpdateText:
// правила те же, что при создании, и на пустом вводе репозиторий не трогаем.
func TestServiceUpdateTextNormalization(t *testing.T) {
	createdAt := svcTime.Add(-time.Hour)
	stored := Note{ID: "id-1", Text: "stored", CreatedAt: createdAt, UpdatedAt: createdAt}

	tests := []struct {
		name     string
		input    string
		wantErr  error
		wantText string
	}{
		{name: "plain text replaces the old one", input: "updated", wantText: "updated"},
		{name: "surrounding spaces are trimmed", input: "   updated   ", wantText: "updated"},
		{name: "newlines and tabs are trimmed", input: "\n\t updated \t\n", wantText: "updated"},
		{name: "inner spaces are preserved", input: "  a  b  ", wantText: "a  b"},
		{name: "unicode text survives", input: " заметка ", wantText: "заметка"},
		{name: "empty string is rejected", input: "", wantErr: ErrEmptyText},
		{name: "spaces only are rejected", input: "     ", wantErr: ErrEmptyText},
		{name: "tabs and newlines only are rejected", input: "\t\n\r ", wantErr: ErrEmptyText},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newRecordingRepo()
			repo.inner.notes[stored.ID] = stored
			svc := newTestService(repo)

			got, err := svc.UpdateText(context.Background(), stored.ID, tt.input)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("UpdateText() err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				if got != (Note{}) {
					t.Errorf("UpdateText() note = %+v, want zero value on error", got)
				}
				if repo.calls != 0 {
					t.Errorf("repo called %d times on invalid input, want 0", repo.calls)
				}
				if repo.inner.notes[stored.ID] != stored {
					t.Errorf("заметка изменилась при невалидном вводе: %+v", repo.inner.notes[stored.ID])
				}
				return
			}

			if got.Text != tt.wantText {
				t.Errorf("Text = %q, want %q", got.Text, tt.wantText)
			}
			if repo.inner.notes[stored.ID].Text != tt.wantText {
				t.Errorf("stored Text = %q, want %q (сервис сохранил ненормализованный текст)", repo.inner.notes[stored.ID].Text, tt.wantText)
			}
		})
	}
}

// TestServiceUpdateTextTimestamps фиксирует правило: UpdatedAt берётся из Clock
// и сдвигается, а id и CreatedAt заметки остаются прежними.
func TestServiceUpdateTextTimestamps(t *testing.T) {
	createdAt := svcTime.Add(-time.Hour)
	stored := Note{ID: "id-1", Text: "stored", CreatedAt: createdAt, UpdatedAt: createdAt}

	repo := newRecordingRepo()
	repo.inner.notes[stored.ID] = stored
	svc := newTestService(repo)

	got, err := svc.UpdateText(context.Background(), stored.ID, "updated")
	if err != nil {
		t.Fatalf("UpdateText() err = %v", err)
	}

	if got.ID != stored.ID {
		t.Errorf("ID = %q, want %q — id заметки менять нельзя", got.ID, stored.ID)
	}
	if !got.CreatedAt.Equal(createdAt) {
		t.Errorf("CreatedAt = %v, want %v — время создания менять нельзя", got.CreatedAt, createdAt)
	}
	if !got.UpdatedAt.Equal(svcTime) {
		t.Errorf("UpdatedAt = %v, want %v (время должно приходить из Clock)", got.UpdatedAt, svcTime)
	}
	if !got.UpdatedAt.After(got.CreatedAt) {
		t.Errorf("UpdatedAt (%v) не сдвинулся относительно CreatedAt (%v)", got.UpdatedAt, got.CreatedAt)
	}
	if repo.gotNote != got {
		t.Errorf("repo got %+v, want %+v — сервис вернул не то, что сохранил", repo.gotNote, got)
	}
	if repo.inner.notes[stored.ID] != got {
		t.Errorf("stored note = %+v, want %+v", repo.inner.notes[stored.ID], got)
	}
}

// TestServiceUpdateTextErrors проверяет, что ошибки чтения и записи доходят
// наверх как есть и не превращаются в частично заполненную заметку.
func TestServiceUpdateTextErrors(t *testing.T) {
	repoErr := errors.New("storage unavailable")

	tests := []struct {
		name    string
		id      string
		seed    bool
		repoErr error
		wantErr error
	}{
		{name: "unknown id is ErrNotFound", id: "nope", seed: true, wantErr: ErrNotFound},
		{name: "empty repo is ErrNotFound", id: "id-1", wantErr: ErrNotFound},
		{name: "repo error is propagated", id: "id-1", seed: true, repoErr: repoErr, wantErr: repoErr},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newRecordingRepo()
			if tt.seed {
				repo.inner.notes["id-1"] = Note{ID: "id-1", Text: "stored", CreatedAt: svcTime, UpdatedAt: svcTime}
			}
			repo.err = tt.repoErr
			svc := newTestService(repo)

			got, err := svc.UpdateText(context.Background(), tt.id, "updated")
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("UpdateText() err = %v, want %v", err, tt.wantErr)
			}
			if got != (Note{}) {
				t.Errorf("UpdateText() note = %+v, want zero value on error", got)
			}
		})
	}
}

// failingUpdateRepo падает только на записи: Get проходит, Update возвращает ошибку.
type failingUpdateRepo struct {
	*recordingRepo
	err error
}

func (r *failingUpdateRepo) Update(_ context.Context, _ Note) error { return r.err }

// TestServiceUpdateTextWriteError покрывает вторую ветку ошибки: заметка
// нашлась, но сохранить её не удалось.
func TestServiceUpdateTextWriteError(t *testing.T) {
	repoErr := errors.New("storage unavailable")
	inner := newRecordingRepo()
	inner.inner.notes["id-1"] = Note{ID: "id-1", Text: "stored", CreatedAt: svcTime, UpdatedAt: svcTime}
	svc := newTestService(&failingUpdateRepo{recordingRepo: inner, err: repoErr})

	got, err := svc.UpdateText(context.Background(), "id-1", "updated")
	if !errors.Is(err, repoErr) {
		t.Fatalf("UpdateText() err = %v, want %v", err, repoErr)
	}
	if got != (Note{}) {
		t.Errorf("UpdateText() note = %+v, want zero value on repo error", got)
	}
}

// TestServicePassThrough проверяет делегирование Get/Delete/List в репозиторий:
// сами аргументы и ошибки не искажаются.
func TestServicePassThrough(t *testing.T) {
	stored := Note{ID: "id-1", Text: "stored", CreatedAt: svcTime, UpdatedAt: svcTime}
	repoErr := errors.New("storage unavailable")

	tests := []struct {
		name    string
		seed    bool
		repoErr error
		call    func(ctx context.Context, svc *Service) error
		wantErr error
	}{
		{
			name: "Get returns stored note",
			seed: true,
			call: func(ctx context.Context, svc *Service) error {
				n, err := svc.Get(ctx, "id-1")
				if err == nil && n != stored {
					return errors.New("Get returned unexpected note")
				}
				return err
			},
		},
		{
			name: "Get maps missing note to ErrNotFound",
			call: func(ctx context.Context, svc *Service) error {
				_, err := svc.Get(ctx, "id-1")
				return err
			},
			wantErr: ErrNotFound,
		},
		{
			name:    "Get propagates repo error",
			repoErr: repoErr,
			call: func(ctx context.Context, svc *Service) error {
				_, err := svc.Get(ctx, "id-1")
				return err
			},
			wantErr: repoErr,
		},
		{
			name: "Delete removes stored note",
			seed: true,
			call: func(ctx context.Context, svc *Service) error { return svc.Delete(ctx, "id-1") },
		},
		{
			name:    "Delete on missing note is ErrNotFound",
			call:    func(ctx context.Context, svc *Service) error { return svc.Delete(ctx, "id-1") },
			wantErr: ErrNotFound,
		},
		{
			name:    "Delete propagates repo error",
			repoErr: repoErr,
			call:    func(ctx context.Context, svc *Service) error { return svc.Delete(ctx, "id-1") },
			wantErr: repoErr,
		},
		{
			name: "List returns notes",
			seed: true,
			call: func(ctx context.Context, svc *Service) error {
				got, err := svc.List(ctx)
				if err != nil {
					return err
				}
				if len(got) != 1 || got[0] != stored {
					return errors.New("List returned unexpected notes")
				}
				return nil
			},
		},
		{
			name: "List on empty repo returns no notes",
			call: func(ctx context.Context, svc *Service) error {
				got, err := svc.List(ctx)
				if err != nil {
					return err
				}
				if len(got) != 0 {
					return errors.New("List returned notes from empty repo")
				}
				return nil
			},
		},
		{
			name:    "List propagates repo error",
			repoErr: repoErr,
			call: func(ctx context.Context, svc *Service) error {
				_, err := svc.List(ctx)
				return err
			},
			wantErr: repoErr,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newRecordingRepo()
			if tt.seed {
				repo.inner.notes[stored.ID] = stored
			}
			repo.err = tt.repoErr
			svc := newTestService(repo)

			err := tt.call(context.Background(), svc)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if repo.calls != 1 {
				t.Errorf("repo called %d times, want 1", repo.calls)
			}
		})
	}
}

// TestServicePassesContext фиксирует обязательный паттерн: сервис прокладывает
// пришедший ctx в репозиторий, а не создаёт context.Background() внутри.
func TestServicePassesContext(t *testing.T) {
	type ctxKey struct{}

	tests := []struct {
		name string
		call func(ctx context.Context, svc *Service)
	}{
		{name: "Create", call: func(ctx context.Context, svc *Service) { _, _ = svc.Create(ctx, "hello") }},
		{name: "Get", call: func(ctx context.Context, svc *Service) { _, _ = svc.Get(ctx, "id-1") }},
		{name: "UpdateText", call: func(ctx context.Context, svc *Service) { _, _ = svc.UpdateText(ctx, "id-1", "hello") }},
		{name: "Delete", call: func(ctx context.Context, svc *Service) { _ = svc.Delete(ctx, "id-1") }},
		{name: "List", call: func(ctx context.Context, svc *Service) { _, _ = svc.List(ctx) }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newRecordingRepo()
			svc := newTestService(repo)
			ctx := context.WithValue(context.Background(), ctxKey{}, "marker")

			tt.call(ctx, svc)

			if repo.gotCtx == nil {
				t.Fatal("repository was not called")
			}
			if got := repo.gotCtx.Value(ctxKey{}); got != "marker" {
				t.Fatalf("repo ctx value = %v, want %q — контекст вызова не проброшен", got, "marker")
			}
		})
	}
}

// TestServiceRespectsCanceledContext проверяет, что отменённый контекст
// доходит до репозитория и его ошибка возвращается вызывающему.
func TestServiceRespectsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	repo := newRecordingRepo()
	repo.err = ctx.Err()
	svc := newTestService(repo)

	if _, err := svc.Create(ctx, "hello"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Create() err = %v, want %v", err, context.Canceled)
	}
	if repo.gotCtx.Err() == nil {
		t.Fatal("репозиторий получил неотменённый контекст")
	}
}
