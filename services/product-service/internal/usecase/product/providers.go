package product

import (
	"product-service/internal/ports"
)

// ProvideUseCaseDependencies provides the dependencies for use cases.
func ProvideUseCaseDependencies(
	productRepo ports.ProductRepository,
	variantRepo ports.VariantRepository,
	categoryLookup ports.CategoryLookup,
	mediaLookup ports.MediaLookup,
	eventRecorder ports.EventRecorder,
	eventSink ports.EventSink,
	outboxRecorder ports.OutboxEventRecorder,
	defaultCurrency string,
) UseCaseDependencies {
	return UseCaseDependencies{
		ProductRepo:     productRepo,
		VariantRepo:     variantRepo,
		CategoryLookup:  categoryLookup,
		MediaLookup:     mediaLookup,
		EventRecorder:   eventRecorder,
		EventSink:       eventSink,
		OutboxRecorder:  outboxRecorder,
		DefaultCurrency: defaultCurrency,
	}
}
