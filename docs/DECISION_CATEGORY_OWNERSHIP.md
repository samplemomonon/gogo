# Category ownership

**Status:** implemented  
**Scope:** global categories and merchant-local categories

## Decision

- Global categories are shared reference data and are created or changed by an
  administrator through category-service's administrative API.
- A seller may create categories for the resolved merchant only. The seller
  interface does not expose global category mutations.
- Category-service stores both scopes. A category with no `tenant_id` is global;
  a category with a `tenant_id` belongs only to that merchant.
- Reads for a merchant return global categories plus that merchant's local
  categories. A merchant cannot read another merchant's local categories.
- A product may reference a global category or a category owned by its tenant.
  Product-service forwards the resolved tenant when it validates category IDs.
- Local category trees stay within the merchant scope. This avoids a global
  category deletion cascading into merchant-owned categories.

## Rendering and ownership

The Seller page owns the category form and list presentation. The category
service owns persistence and hierarchy rules. Merchant category creation crosses
the service's authenticated gRPC contract; the merchant ID comes from trusted
tenant metadata, never from a form field. Category IDs remain owned by
category-service and are validated again by product-service when a product is
created or updated.

Global categories are marked as administrator-managed in Seller. Local
categories are marked as belonging to the current merchant, and are the only
categories available as parents in the local category form.
