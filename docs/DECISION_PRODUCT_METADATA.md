# Product metadata ownership and lifecycle

**Status:** initial vertical slice implemented; admin-managed schema is the target for the next product capability track  
**Scope:** Product Type definitions, Attribute values, product creation, projections, and storefront rendering

## Ownership

- Product Service owns product type definitions, schema versions, validation, and persisted attribute values.
- The seller UI requests the schema over `product.v1`; it does not duplicate the attribute rules.
- Product Service publishes type/version/values in the existing product snapshot event.
- Listing and Search own independent projections of those values. The SSR product detail page reads Listing and makes no per-request call to Product Service.
- Platform UI primitives own generic controls, not product terminology or required-field rules.

## Current implementation

The first types are `apparel` and `general`. Apparel has optional descriptive `material` and non-negative numeric `weight` (grams). Color and size are intentionally not product-level attributes: they describe purchasable variants and belong with structured variant options when that workflow is implemented.

The initial slice stores schema definitions in a code-owned Product Service registry. Product records store the type and schema version used at creation; Seller forms submit that version; Product Service validates attribute values. This is a working integration baseline, not the desired authoring workflow.

## Target admin workflow

Product Service owns schema persistence, draft and publish lifecycle, versioning, and validation. An authorized administrator edits a draft, validates and previews it, then publishes an immutable schema version. Seller discovers the published schema through the existing product contract. Product Service validates product writes against the submitted published version and stores that version with the product.

Keep validation mechanics and universal invariants in Product Service code. Admins select supported field types and constraints; they cannot upload executable rules. Changes to a schema do not silently rewrite or invalidate existing products. Use an explicit migration or compatibility policy for incompatible changes.

No new repository or service is required for this workflow. Revisit that decision when a distinct team or deployment lifecycle owns catalog schemas, or when several independent systems need to author and consume a shared catalog contract.

## Separate content ownership

Use a headless CMS for banners and editorial page content, with an allowlisted block contract rendered by the gateway SSR. Use Rspress for developer documentation authored and reviewed in Git. Neither owns product schema validation.

Search currently carries metadata in its read document but does not expose attribute filters/facets. Add those only for attributes with an agreed query use case and index strategy.
