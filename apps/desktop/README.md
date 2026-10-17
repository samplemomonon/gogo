# Desktop application boundary

This directory is reserved for the desktop client. The first implementation
should establish one runnable vertical slice before adding a framework-wide
native component layer. Shared visual intent is owned by the repository-root
[`design-system`](../../design-system/README.md); the desktop client should map
its semantic tokens to native controls and layout APIs.

The desktop app owns desktop navigation, lifecycle, window behavior, and desktop
presentation. It consumes backend capabilities through versioned service
APIs/contracts. It must not import Go `internal` packages or depend on web
templates and CSS components.

The framework and client generation approach are intentionally undecided. Choose
them when implementing the first desktop workflow, then add the app manifest,
build/run instructions, CI, and a platform-specific UI primitive layer here.
Keep window behavior, desktop navigation, accessibility behavior, and keyboard
interaction native; share token roles and component intent, not web markup or
CSS classes.
