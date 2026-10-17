# Design system

The platform-owned design system has moved to the repository root at
[`design-system/`](../design-system/README.md). That directory owns the
cross-platform token source, component library, web adapter, release policy and
contribution governance. The web app consumes its web primitives as a separate
Go module. Mobile and desktop adapters map the same semantic tokens when those
clients become runnable.
