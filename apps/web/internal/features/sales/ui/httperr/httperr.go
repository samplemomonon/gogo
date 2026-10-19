// Package httperr maps the seller console's remote-peer errors to an HTTP status
// and a stable, cashier-facing message. The console is a *consumer*: it does not
// own the catalog, inventory or sales vocabularies, so it names their rejections
// by the sentinels its own adapters project and never renders an upstream
// message or a Go error string into the page.
//
// The table is deliberately explicit. A business rejection is the peer's answer
// and may be shown; an outage is not, so every unavailable sentinel collapses to
// one honest "try again" message that hides which dependency is down.
package httperr

import (
	"errors"
	"net/http"

	"gogo/internal/features/sales/ui/infrastructure/inventoryclient"
	"gogo/internal/features/sales/ui/infrastructure/salesclient"
	"gogo/internal/shared/productconsumer"
)

// Messages are written for the cashier at the till, not for a log reader.
const (
	// msgUnavailable is the single message for every unreachable dependency.
	msgUnavailable = "Layanan sedang tidak tersedia, coba lagi sebentar lagi"

	msgProductNotFound = "Produk tidak ditemukan"
	msgInvalidProduct  = "Data produk tidak valid"
	msgProductExists   = "Produk dengan identitas tersebut sudah ada"
	msgNotAllowed      = "Operasi tidak diizinkan pada data saat ini"
	// One message for the whole platform: the cashier cannot act on which of the
	// four services is down, and naming it would leak the topology into the till UI.
	msgProductDown = msgUnavailable

	msgUnknownProduct = "Produk tidak dikenal oleh layanan stok"
	msgInvalidAdjust  = "Penyesuaian stok tidak valid"
	msgInsufficient   = "Stok tidak mencukupi"
	msgInventoryDown  = msgUnavailable

	msgTransactionNotFound = "Transaksi tidak ditemukan"
	msgInvalidCheckout     = "Keranjang tidak valid"
	msgSaleRefused         = "Penjualan ditolak"
	msgSalesDown           = msgUnavailable
)

// entry is one row of the table: a peer rejection, and how the console answers
// it. A nil status means "not a recognised peer answer" and falls through to
// the default.
type entry struct {
	err     error
	status  int
	message string
}

var table = []entry{
	// The shared product-platform vocabulary, passed through by the seller adapter.
	{productconsumer.ErrNotFound, http.StatusNotFound, msgProductNotFound},
	{productconsumer.ErrInvalid, http.StatusBadRequest, msgInvalidProduct},
	{productconsumer.ErrConflict, http.StatusConflict, msgProductExists},
	{productconsumer.ErrFailedPrecondition, http.StatusConflict, msgNotAllowed},
	{productconsumer.ErrProductPlatformUnavailable, http.StatusServiceUnavailable, msgProductDown},

	// The inventory vocabulary.
	{inventoryclient.ErrUnknownProduct, http.StatusNotFound, msgUnknownProduct},
	{inventoryclient.ErrInvalidAdjustment, http.StatusBadRequest, msgInvalidAdjust},
	{inventoryclient.ErrInsufficientStock, http.StatusConflict, msgInsufficient},
	{inventoryclient.ErrInventoryUnavailable, http.StatusServiceUnavailable, msgInventoryDown},

	// The sales vocabulary.
	{salesclient.ErrTransactionNotFound, http.StatusNotFound, msgTransactionNotFound},
	{salesclient.ErrInvalidCheckout, http.StatusBadRequest, msgInvalidCheckout},
	{salesclient.ErrSaleRefused, http.StatusConflict, msgSaleRefused},
	{salesclient.ErrSalesUnavailable, http.StatusServiceUnavailable, msgSalesDown},
}

// Map converts a peer error into the status and message the console shows. An
// unrecognised error is a bug in the console's own code, so it is reported as
// an internal failure with a fixed message rather than a leaked error string.
func Map(err error) (int, string) {
	if err == nil {
		return http.StatusOK, ""
	}
	for _, row := range table {
		if errors.Is(err, row.err) {
			return row.status, row.message
		}
	}
	return http.StatusInternalServerError, "Terjadi kesalahan pada konsol"
}

// Message returns just the cashier-facing text for err, which is what an inline
// form alert needs. It deliberately drops the status: a re-rendered page keeps
// its 200 so the form keeps the cashier's input on screen.
func Message(err error) string {
	if errors.Is(err, productconsumer.ErrInvalid) {
		if detail := productconsumer.Detail(err); detail != "" {
			return msgInvalidProduct + ": " + detail
		}
	}
	_, message := Map(err)
	return message
}

// Unavailable reports whether err is an upstream outage rather than a business
// answer, so a handler can log it and decide to degrade instead of arguing with
// the cashier about a form.
func Unavailable(err error) bool {
	status, _ := Map(err)
	return status == http.StatusServiceUnavailable
}
