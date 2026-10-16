# Mobile application boundary

This directory is reserved for the mobile client. The first implementation
should establish one runnable vertical slice before adding a framework-wide
shared UI layer.

The mobile app owns native navigation, lifecycle, permissions, offline behavior,
and mobile presentation. It consumes backend capabilities through versioned
service APIs/contracts. It must not import Go `internal` packages or depend on
web templates and CSS components.

The framework and client generation approach are intentionally undecided. Choose
them when implementing the first mobile workflow, then add the app manifest,
build/run instructions, CI, and a platform-specific UI primitive layer here.
