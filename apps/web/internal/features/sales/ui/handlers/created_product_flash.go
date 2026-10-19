package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"gogo/internal/features/sales/ui/application/results"
	"gogo/internal/shared/tenant"
)

const (
	createdProductFlashTTL = 2 * time.Minute
	createdProductFlashMax = 256
)

type createdProductFlashKey struct {
	tenant tenant.ID
	token  string
}

type createdProductFlash struct {
	product *results.Product
	expires time.Time
}

// createdProductFlashes bridges the write-side result across the seller's
// Post/Redirect/Get flow while the asynchronous listing projection catches up.
// Keys are one-time random tokens scoped to the resolved tenant.
type createdProductFlashes struct {
	mu    sync.Mutex
	items map[createdProductFlashKey]createdProductFlash
}

func newCreatedProductFlashes() *createdProductFlashes {
	return &createdProductFlashes{items: make(map[createdProductFlashKey]createdProductFlash)}
}

func (f *createdProductFlashes) put(tenantID tenant.ID, product *results.Product) (string, error) {
	if product == nil {
		return "", errors.New("created product is nil")
	}
	var tokenBytes [16]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		return "", err
	}
	token := hex.EncodeToString(tokenBytes[:])
	now := time.Now()

	f.mu.Lock()
	defer f.mu.Unlock()
	f.prune(now)
	if len(f.items) >= createdProductFlashMax {
		f.removeOldest()
	}
	f.items[createdProductFlashKey{tenant: tenantID, token: token}] = createdProductFlash{
		product: product,
		expires: now.Add(createdProductFlashTTL),
	}
	return token, nil
}

func (f *createdProductFlashes) take(tenantID tenant.ID, token string) *results.Product {
	if token == "" {
		return nil
	}
	key := createdProductFlashKey{tenant: tenantID, token: token}
	now := time.Now()
	f.mu.Lock()
	defer f.mu.Unlock()
	entry, ok := f.items[key]
	delete(f.items, key)
	if !ok || !now.Before(entry.expires) {
		return nil
	}
	return entry.product
}

func (f *createdProductFlashes) prune(now time.Time) {
	for key, entry := range f.items {
		if !now.Before(entry.expires) {
			delete(f.items, key)
		}
	}
}

func (f *createdProductFlashes) removeOldest() {
	var oldestKey createdProductFlashKey
	var oldest time.Time
	for key, entry := range f.items {
		if oldest.IsZero() || entry.expires.Before(oldest) {
			oldestKey, oldest = key, entry.expires
		}
	}
	if !oldest.IsZero() {
		delete(f.items, oldestKey)
	}
}
