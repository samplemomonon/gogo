// Package repository_test pins how persistence failures reach the layers
// above. Every method wraps the driver error with the operation and the id, so
// a log line names what was attempted; and the two ErrNotFound paths — a
// missing row in GetByID and an update that matched nothing — must never
// classify a connection failure as a missing row.
//
// The tests are hermetic: the pool points at a closed port, so every query
// fails with a dial error and no PostgreSQL server is required.
package repository_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"category-service/internal/apperror"
	"category-service/internal/model"
	"category-service/internal/repository"

	_ "github.com/lib/pq" // register the "postgres" driver
)

// unreachableDSN points at a closed port so an attempted use fails with a dial
// error instead of hanging; connect_timeout bounds it even if the port starts
// answering.
const unreachableDSN = "postgres://user:pass@127.0.0.1:1/db?connect_timeout=1&sslmode=disable"

// unreachableRepository builds a live repository over an unreachable server.
func unreachableRepository(t *testing.T) *repository.CategoryRepository {
	t.Helper()

	db, err := sql.Open("postgres", unreachableDSN)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	return repository.NewCategoryRepository(db)
}

func testContext(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// updateRequest builds a request the way the HTTP layer does — through
// UnmarshalJSON — because the presence flags that drive the SET clause are
// recorded there, not by struct literals.
func updateRequest(t *testing.T, body string) *model.UpdateCategoryRequest {
	t.Helper()

	req := &model.UpdateCategoryRequest{}
	if err := req.UnmarshalJSON([]byte(body)); err != nil {
		t.Fatalf("decode update request %s: %v", body, err)
	}
	return req
}

// TestFailuresAreWrappedWithTheOperationAndID checks the message contract
// method by method: an operator reading the log knows which query ran and
// against which row.
func TestFailuresAreWrappedWithTheOperationAndID(t *testing.T) {
	t.Parallel()

	repo := unreachableRepository(t)
	ctx := testContext(t)

	name := "Pakaian"
	parentID := int64(3)

	tests := []struct {
		name string
		call func() error
		want string
	}{
		{
			name: "Create",
			call: func() error {
				_, err := repo.Create(ctx, &model.CreateCategoryRequest{Name: name, ParentID: &parentID})
				return err
			},
			want: "create category:",
		},
		{
			name: "GetByID",
			call: func() error {
				_, err := repo.GetByID(ctx, 7)
				return err
			},
			want: "get category 7:",
		},
		{
			name: "GetAll",
			call: func() error {
				_, err := repo.GetAll(ctx)
				return err
			},
			want: "list categories:",
		},
		{
			name: "GetChildren",
			call: func() error {
				_, err := repo.GetChildren(ctx, 7)
				return err
			},
			want: "list children of category 7:",
		},
		{
			name: "GetAncestors",
			call: func() error {
				_, err := repo.GetAncestors(ctx, 7)
				return err
			},
			want: "list ancestors of category 7:",
		},
		{
			name: "Update",
			call: func() error {
				_, err := repo.Update(ctx, 7, updateRequest(t, `{"name":"Pakaian"}`))
				return err
			},
			want: "update category 7:",
		},
		{
			name: "Delete",
			call: func() error {
				return repo.Delete(ctx, 7)
			},
			want: "delete category 7:",
		},
		{
			name: "WouldCreateCircularReference",
			call: func() error {
				_, err := repo.WouldCreateCircularReference(ctx, 7, 3)
				return err
			},
			want: "check circular reference for category 7:",
		},
		{
			name: "CountDescendants",
			call: func() error {
				_, err := repo.CountDescendants(ctx, 7)
				return err
			},
			want: "count descendants of category 7:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.call()
			if err == nil {
				t.Fatal("expected an error from an unreachable server")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q should contain %q", err, tt.want)
			}
		})
	}
}

// TestConnectionFailuresAreNotReportedAsNotFound is the classification rule:
// only sql.ErrNoRows may become ErrNotFound, so a dead database can never be
// answered as a 404. The check covers GetByID, Update and Delete — the three
// methods with a not-found branch.
func TestConnectionFailuresAreNotReportedAsNotFound(t *testing.T) {
	t.Parallel()

	repo := unreachableRepository(t)
	ctx := testContext(t)

	for _, tt := range []struct {
		name string
		call func() error
	}{
		{
			name: "GetByID",
			call: func() error {
				_, err := repo.GetByID(ctx, 7)
				return err
			},
		},
		{
			name: "Update",
			call: func() error {
				_, err := repo.Update(ctx, 7, updateRequest(t, `{"name":"Pakaian"}`))
				return err
			},
		},
		{
			name: "Delete",
			call: func() error {
				return repo.Delete(ctx, 7)
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.call()
			if err == nil {
				t.Fatal("expected an error from an unreachable server")
			}
			if errors.Is(err, apperror.ErrNotFound) {
				t.Errorf("dial failure %q classified as ErrNotFound", err)
			}
		})
	}
}

// TestUpdateWithoutFieldsReadsInsteadOfWriting pins the documented no-op path:
// an empty request falls through to GetByID, which is observable because it
// fails with the read's message rather than the update's.
func TestUpdateWithoutFieldsReadsInsteadOfWriting(t *testing.T) {
	t.Parallel()

	repo := unreachableRepository(t)

	_, err := repo.Update(testContext(t), 7, &model.UpdateCategoryRequest{})
	if err == nil {
		t.Fatal("expected an error from an unreachable server")
	}
	if !strings.Contains(err.Error(), "get category 7:") {
		t.Errorf("empty update error %q should be the read's, not an UPDATE statement's", err)
	}
}

// TestMultiRowQueriesReturnNoRows pins the shape contract for the list
// methods: on failure the result is nil rather than a partial slice, so a
// caller that forgets the error cannot mistake a failure for an empty list.
func TestMultiRowQueriesReturnNoRows(t *testing.T) {
	t.Parallel()

	repo := unreachableRepository(t)
	ctx := testContext(t)

	for _, tt := range []struct {
		name string
		call func() ([]*model.Category, error)
	}{
		{"GetAll", func() ([]*model.Category, error) { return repo.GetAll(ctx) }},
		{"GetChildren", func() ([]*model.Category, error) { return repo.GetChildren(ctx, 7) }},
		{"GetAncestors", func() ([]*model.Category, error) { return repo.GetAncestors(ctx, 7) }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := tt.call()
			if err == nil {
				t.Fatal("expected an error from an unreachable server")
			}
			if got != nil {
				t.Errorf("result = %v, want nil alongside the error", got)
			}
		})
	}
}
