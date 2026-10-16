package grpcsrv

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"product-search-service/gen/searchpb"
	"product-search-service/internal/apperror"
	"product-search-service/internal/model"
)

// stubSearch records the query and returns a fixed page or error.
type stubSearch struct {
	gotQuery *model.SearchQuery
	err      error
}

func (s *stubSearch) Search(_ context.Context, q *model.SearchQuery) (*model.SearchResult, error) {
	s.gotQuery = q
	if s.err != nil {
		return nil, s.err
	}
	return &model.SearchResult{
		Products: []*model.SearchProduct{{
			ID: 1, CategoryID: 2, Name: "Aurora", Slug: "aurora",
			Brand: strPtr("Zed"), Score: 0.87, Status: model.StatusActive,
		}},
		Total: 1, Limit: q.Limit, Offset: q.Offset, TookMS: 3,
	}, nil
}

// startServer runs the real generated server over a real listener, so the test
// covers the adapter's conversion both ways rather than just its methods.
func startServer(t *testing.T, svc ProductSearchService) searchpb.ProductSearchServiceClient {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	server := grpc.NewServer()
	searchpb.RegisterProductSearchServiceServer(server,
		NewServer(svc, slog.New(slog.NewTextHandler(io.Discard, nil))))

	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	conn, err := grpc.NewClient(listener.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return searchpb.NewProductSearchServiceClient(conn)
}

func TestSearchProductsConvertsBothWays(t *testing.T) {
	t.Parallel()

	svc := &stubSearch{}
	client := startServer(t, svc)

	brand := "Zed"
	resp, err := client.SearchProducts(context.Background(), &searchpb.SearchProductsRequest{
		Query:      "aurora",
		CategoryId: 2,
		Status:     searchpb.ProductStatus_PRODUCT_STATUS_ACTIVE,
		Limit:      10,
		Offset:     5,
	})
	if err != nil {
		t.Fatalf("SearchProducts: %v", err)
	}

	// Request converted to the domain query.
	q := svc.gotQuery
	switch {
	case q.Query != "aurora":
		t.Errorf("query = %q, want aurora", q.Query)
	case q.CategoryID != 2:
		t.Errorf("category = %d, want 2", q.CategoryID)
	case q.Limit != 10 || q.Offset != 5:
		t.Errorf("page = %d+%d, want 10+5", q.Limit, q.Offset)
	case q.Status == nil || *q.Status != model.StatusActive:
		t.Errorf("status = %v, want active", q.Status)
	}

	// Response converted back to protobuf.
	if len(resp.GetProducts()) != 1 {
		t.Fatalf("got %d products, want 1", len(resp.GetProducts()))
	}
	product := resp.GetProducts()[0]
	if product.GetId() != 1 || product.GetName() != "Aurora" {
		t.Errorf("product = %+v, want the document", product)
	}
	if product.GetScore() != 0.87 {
		t.Errorf("score = %v, want 0.87", product.GetScore())
	}
	if product.GetStatus() != searchpb.ProductStatus_PRODUCT_STATUS_ACTIVE {
		t.Errorf("status = %v, want active", product.GetStatus())
	}
	if product.GetBrand() != brand {
		t.Errorf("brand = %q, want %q", product.GetBrand(), brand)
	}
	if resp.GetTotal() != 1 || resp.GetLimit() != 10 || resp.GetOffset() != 5 {
		t.Errorf("page echoed as %d/%d+%d, want 1/10+5", resp.GetTotal(), resp.GetLimit(), resp.GetOffset())
	}
	if resp.GetTookMs() != 3 {
		t.Errorf("took_ms = %d, want 3", resp.GetTookMs())
	}
	if product.GetCreatedAt() == nil {
		t.Error("created_at was not converted")
	}
}

// An unspecified status is "no filter". Treating the zero value as draft would hide
// every active product, which is the same trap the HTTP handler avoids.
func TestUnspecifiedStatusMeansNoFilter(t *testing.T) {
	t.Parallel()

	svc := &stubSearch{}
	client := startServer(t, svc)

	if _, err := client.SearchProducts(context.Background(),
		&searchpb.SearchProductsRequest{Query: "aurora"}); err != nil {
		t.Fatalf("SearchProducts: %v", err)
	}
	if svc.gotQuery.Status != nil {
		t.Errorf("status = %q, want nil for an unspecified filter", *svc.gotQuery.Status)
	}
}

// A caller has to be able to branch on the code, so a domain failure has to arrive
// as the right gRPC status rather than as Internal.
func TestDomainErrorsBecomeStatusCodes(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		err  error
		want codes.Code
	}{
		"invalid input": {apperror.ErrInvalidInput, codes.InvalidArgument},
		"not found":     {apperror.ErrNotFound, codes.NotFound},
		// A read model that is behind is worth retrying, which a validation
		// failure is not.
		"unavailable": {apperror.ErrUnavailable, codes.Unavailable},
		"unknown":     {errors.New("boom"), codes.Internal},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			client := startServer(t, &stubSearch{err: tc.err})
			_, err := client.SearchProducts(context.Background(),
				&searchpb.SearchProductsRequest{Query: "aurora"})
			if err == nil {
				t.Fatal("err = nil, want a failure")
			}
			if got := status.Code(err); got != tc.want {
				t.Errorf("code = %v, want %v", got, tc.want)
			}
		})
	}
}

// The internal cause must not travel to a peer on an unrecognised failure.
func TestInternalErrorsDoNotLeakDetail(t *testing.T) {
	t.Parallel()

	client := startServer(t, &stubSearch{err: errors.New("connection to 10.0.0.5 refused")})
	_, err := client.SearchProducts(context.Background(),
		&searchpb.SearchProductsRequest{Query: "aurora"})
	if err == nil {
		t.Fatal("err = nil, want a failure")
	}
	if got := status.Convert(err).Message(); got == "connection to 10.0.0.5 refused" {
		t.Error("the internal error text was sent to the caller")
	}
}

func strPtr(s string) *string { return &s }
