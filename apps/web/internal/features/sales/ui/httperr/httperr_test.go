package httperr_test

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"gogo/internal/features/sales/ui/httperr"
	"gogo/internal/features/sales/ui/infrastructure/inventoryclient"
	"gogo/internal/features/sales/ui/infrastructure/salesclient"
	"gogo/internal/shared/productconsumer"
)

func TestMapNamesEveryBusinessAnswer(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		err         error
		wantStatus  int
		wantMessage string
	}{
		{"product not found", productconsumer.ErrNotFound, http.StatusNotFound, "Produk tidak ditemukan"},
		{"product invalid", productconsumer.ErrInvalid, http.StatusBadRequest, "Data produk tidak valid"},
		{"product conflict", productconsumer.ErrConflict, http.StatusConflict, "Produk dengan identitas tersebut sudah ada"},
		{"product precondition", productconsumer.ErrFailedPrecondition, http.StatusConflict, "Operasi tidak diizinkan pada data saat ini"},
		{"unknown product", inventoryclient.ErrUnknownProduct, http.StatusNotFound, "Produk tidak dikenal oleh layanan stok"},
		{"invalid adjustment", inventoryclient.ErrInvalidAdjustment, http.StatusBadRequest, "Penyesuaian stok tidak valid"},
		{"insufficient stock", inventoryclient.ErrInsufficientStock, http.StatusConflict, "Stok tidak mencukupi"},
		{"transaction not found", salesclient.ErrTransactionNotFound, http.StatusNotFound, "Transaksi tidak ditemukan"},
		{"invalid checkout", salesclient.ErrInvalidCheckout, http.StatusBadRequest, "Keranjang tidak valid"},
		{"sale refused", salesclient.ErrSaleRefused, http.StatusConflict, "Penjualan ditolak"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			status, message := httperr.Map(tc.err)
			if status != tc.wantStatus {
				t.Errorf("status = %d, want %d", status, tc.wantStatus)
			}
			if message != tc.wantMessage {
				t.Errorf("message = %q, want %q", message, tc.wantMessage)
			}
		})
	}
}

// Every outage must collapse to one message: naming which dependency is down
// tells a caller more than the console needs to know, and the three sentinels
// are different errors with the same operator response.
func TestMapCollapsesEveryOutageToOneMessage(t *testing.T) {
	t.Parallel()

	outages := []error{
		productconsumer.ErrProductPlatformUnavailable,
		inventoryclient.ErrInventoryUnavailable,
		salesclient.ErrSalesUnavailable,
	}

	first, _ := httperr.Map(outages[0])
	if first != http.StatusServiceUnavailable {
		t.Fatalf("platform outage status = %d, want %d", first, http.StatusServiceUnavailable)
	}
	for _, err := range outages {
		status, message := httperr.Map(err)
		if status != http.StatusServiceUnavailable {
			t.Errorf("%v: status = %d, want 503", err, status)
		}
		if message != httperr.Message(outages[0]) {
			t.Errorf("%v: message = %q, want the shared outage message %q", err, message, httperr.Message(outages[0]))
		}
		if err.Error() == message {
			t.Errorf("%v: outage leaked its Go error text as the message", err)
		}
	}
}

// A wrapped refusal must still be recognised: adapters may annotate an error
// with the operation that failed, and errors.Is has to see through that.
func TestMapSeesThroughWrappedRefusals(t *testing.T) {
	t.Parallel()

	wrapped := fmt.Errorf("adjust stock: %w", inventoryclient.ErrInsufficientStock)
	if status, _ := httperr.Map(wrapped); status != http.StatusConflict {
		t.Errorf("wrapped refusal status = %d, want %d", status, http.StatusConflict)
	}
}

// An unrecognised error is the console's own bug. It must not leak its text.
func TestMapHidesUnrecognisedErrors(t *testing.T) {
	t.Parallel()

	internal := errors.New("connection reset by peer at 10.0.3.7:9090")
	status, message := httperr.Map(internal)
	if status != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", status, http.StatusInternalServerError)
	}
	if message == internal.Error() {
		t.Error("unrecognised error leaked its Go text as the message")
	}
}

// The table never reads the error text, so an upstream's "rpc error: code = ..."
// cannot reach the cashier.
func TestMapNeverEchoesTheSentinelText(t *testing.T) {
	t.Parallel()

	sentinels := []error{
		productconsumer.ErrNotFound,
		productconsumer.ErrInvalid,
		productconsumer.ErrConflict,
		productconsumer.ErrFailedPrecondition,
		productconsumer.ErrProductPlatformUnavailable,
		inventoryclient.ErrUnknownProduct,
		inventoryclient.ErrInvalidAdjustment,
		inventoryclient.ErrInsufficientStock,
		inventoryclient.ErrInventoryUnavailable,
		salesclient.ErrTransactionNotFound,
		salesclient.ErrInvalidCheckout,
		salesclient.ErrSaleRefused,
		salesclient.ErrSalesUnavailable,
	}
	for _, err := range sentinels {
		if httperr.Message(err) == err.Error() {
			t.Errorf("%v: message is the sentinel's own Go text", err)
		}
	}
}

func TestUnavailableDistinguishesOutageFromRefusal(t *testing.T) {
	t.Parallel()

	if !httperr.Unavailable(salesclient.ErrSalesUnavailable) {
		t.Error("Unavailable = false for an outage, want true")
	}
	if httperr.Unavailable(inventoryclient.ErrInsufficientStock) {
		t.Error("Unavailable = true for a business refusal, want false")
	}
}

func TestMapNilError(t *testing.T) {
	t.Parallel()

	status, message := httperr.Map(nil)
	if status != http.StatusOK || message != "" {
		t.Errorf("Map(nil) = %d, %q; want 200, \"\"", status, message)
	}
}
