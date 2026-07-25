package note

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"
)

// stepClock — часы, которые сдвигаются вперёд на каждом вызове Now.
// В отличие от fixedClock они видят разницу между «время взяли один раз»
// и «время берут отдельно на каждое поле».
type stepClock struct {
	t     time.Time
	step  time.Duration
	calls int
}

func (c *stepClock) Now() time.Time {
	c.calls++
	now := c.t
	c.t = c.t.Add(c.step)
	return now
}

// countingID — генератор, который выдаёт разные id и считает обращения.
type countingID struct{ calls int }

func (g *countingID) NewID() string {
	g.calls++
	return "id-" + strconv.Itoa(g.calls)
}

// TestServiceCreateReadsClockOnce фиксирует инвариант новой заметки:
// CreatedAt и UpdatedAt берутся из одного и того же тика часов, поэтому
// у только что созданной заметки они строго равны.
func TestServiceCreateReadsClockOnce(t *testing.T) {
	clock := &stepClock{t: svcTime, step: time.Second}
	svc := NewService(newFakeRepo(), fixedID{id: "id-1"}, clock)

	got, err := svc.Create(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Create() err = %v", err)
	}

	if clock.calls != 1 {
		t.Errorf("Clock.Now() вызван %d раз, want 1 — метки времени должны быть одним снимком часов", clock.calls)
	}
	if !got.CreatedAt.Equal(got.UpdatedAt) {
		t.Errorf("CreatedAt (%v) != UpdatedAt (%v) у новой заметки", got.CreatedAt, got.UpdatedAt)
	}
	if !got.CreatedAt.Equal(svcTime) {
		t.Errorf("CreatedAt = %v, want %v — время должно приходить из Clock", got.CreatedAt, svcTime)
	}
}

// TestServiceUpdateTextReadsClockOnce проверяет вторую половину правила:
// обновление берёт время один раз и двигает только UpdatedAt.
func TestServiceUpdateTextReadsClockOnce(t *testing.T) {
	clock := &stepClock{t: svcTime, step: time.Minute}
	repo := newFakeRepo()
	svc := NewService(repo, fixedID{id: "id-1"}, clock)

	created, err := svc.Create(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Create() err = %v", err)
	}
	updated, err := svc.UpdateText(context.Background(), created.ID, "updated")
	if err != nil {
		t.Fatalf("UpdateText() err = %v", err)
	}

	if clock.calls != 2 {
		t.Errorf("Clock.Now() вызван %d раз на Create+UpdateText, want 2", clock.calls)
	}
	if !updated.CreatedAt.Equal(created.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v — время создания менять нельзя", updated.CreatedAt, created.CreatedAt)
	}
	if !updated.UpdatedAt.Equal(svcTime.Add(time.Minute)) {
		t.Errorf("UpdatedAt = %v, want %v — второй тик часов", updated.UpdatedAt, svcTime.Add(time.Minute))
	}
	if !updated.UpdatedAt.After(updated.CreatedAt) {
		t.Errorf("UpdatedAt (%v) не сдвинулся вперёд относительно CreatedAt (%v)", updated.UpdatedAt, updated.CreatedAt)
	}
}

// TestServiceDoesNotTouchDepsOnInvalidInput фиксирует порядок работы сервиса:
// валидация идёт первой, поэтому на пустом тексте не тратятся ни id,
// ни отметка времени — независимо от того, существует заметка или нет.
func TestServiceDoesNotTouchDepsOnInvalidInput(t *testing.T) {
	tests := []struct {
		name string
		seed bool
		call func(ctx context.Context, svc *Service) error
	}{
		{
			name: "Create с пустым текстом",
			call: func(ctx context.Context, svc *Service) error {
				_, err := svc.Create(ctx, "   ")
				return err
			},
		},
		{
			name: "UpdateText с пустым текстом на существующей заметке",
			seed: true,
			call: func(ctx context.Context, svc *Service) error {
				_, err := svc.UpdateText(ctx, "seed", "\t\n ")
				return err
			},
		},
		{
			name: "UpdateText с пустым текстом на неизвестном id",
			call: func(ctx context.Context, svc *Service) error {
				_, err := svc.UpdateText(ctx, "nope", "")
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clock := &stepClock{t: svcTime, step: time.Second}
			ids := &countingID{}
			repo := newRecordingRepo()
			if tt.seed {
				repo.inner.notes["seed"] = Note{ID: "seed", Text: "stored", CreatedAt: svcTime, UpdatedAt: svcTime}
			}
			svc := NewService(repo, ids, clock)

			if err := tt.call(context.Background(), svc); !errors.Is(err, ErrEmptyText) {
				t.Fatalf("err = %v, want %v", err, ErrEmptyText)
			}
			if clock.calls != 0 {
				t.Errorf("Clock.Now() вызван %d раз на невалидном вводе, want 0", clock.calls)
			}
			if ids.calls != 0 {
				t.Errorf("IDGenerator.NewID() вызван %d раз на невалидном вводе, want 0", ids.calls)
			}
			if repo.calls != 0 {
				t.Errorf("репозиторий вызван %d раз на невалидном вводе, want 0", repo.calls)
			}
		})
	}
}

// TestServiceCreateAsksIDGeneratorEveryTime проверяет, что каждая заметка
// получает свой идентификатор от IDGenerator, а не переиспользует прошлый.
func TestServiceCreateAsksIDGeneratorEveryTime(t *testing.T) {
	ids := &countingID{}
	repo := newFakeRepo()
	svc := NewService(repo, ids, fixedClock{t: svcTime})

	first, err := svc.Create(context.Background(), "first")
	if err != nil {
		t.Fatalf("first Create() err = %v", err)
	}
	second, err := svc.Create(context.Background(), "second")
	if err != nil {
		t.Fatalf("second Create() err = %v", err)
	}

	if ids.calls != 2 {
		t.Errorf("IDGenerator.NewID() вызван %d раз на две заметки, want 2", ids.calls)
	}
	if first.ID == second.ID {
		t.Fatalf("обе заметки получили id %q — идентификатор не запрашивается заново", first.ID)
	}
	if len(repo.notes) != 2 {
		t.Errorf("в хранилище %d заметок, want 2 — вторая перезаписала первую", len(repo.notes))
	}
}
