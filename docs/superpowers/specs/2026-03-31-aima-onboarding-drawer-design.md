# AIMA Onboarding Drawer Design

Date: 2026-03-31
Status: Approved for planning
Branch: `feat/onboarding-drawer-spec`

## Context

After deployment, first-time users can land on the AIMA Web UI and not understand how to start.
The current UI exposes agent chat, hardware, deployments, fleet, settings, and support connectivity,
but it does not provide an obvious guided entry point for basic usage.

The goal is to add a visible but secondary onboarding entry in the chat header that opens a
right-side drawer with layered guidance. The content should reuse existing AIMA command guidance
and project documentation structure rather than inventing a separate help system.

## User-Approved Decisions

- Entry location: chat header, next to the existing support button
- Entry visual weight: weaker than `连线灵机云`
- Open behavior: right-side drawer
- Content depth: layered
  - quick start first
  - full command guidance available inside the drawer

## Goals

- Give new users an obvious "start here" entry when they first open the UI
- Explain the first useful actions without forcing users to read long documentation
- Reuse mature command guidance already present in the project
- Keep the primary support workflow and existing chat workflow intact
- Stay consistent with current AIMA UI architecture and offline-first constraints

## Non-Goals

- Replacing the support flow
- Replacing the settings modal
- Building a full documentation site inside the UI
- Auto-executing commands on behalf of the user
- Introducing external web dependencies or a Markdown rendering stack

## Approaches Considered

### 1. Pure Frontend Inline Content

Put the drawer UI and all onboarding copy directly into `internal/ui/static/index.html`.

Pros:
- fastest implementation
- no backend changes

Cons:
- content becomes tightly coupled to UI markup
- higher risk of drift from `docs/cli.md` and `catalog/agent-guide.md`
- harder to test or evolve

### 2. Serve Raw Markdown into the Drawer

Expose existing documentation through a new UI API endpoint and render Markdown in the drawer.

Pros:
- closer to a single documentation source

Cons:
- poor fit for a layered onboarding drawer
- requires Markdown rendering and UI sanitization decisions
- likely too text-heavy for first-run guidance

### 3. Structured Onboarding Manifest + Drawer UI

Add a structured onboarding manifest endpoint, similar to the existing support manifest pattern,
and render the drawer from that manifest.

Pros:
- clean separation between content and presentation
- fits existing UI route/dependency architecture
- easier i18n and testing
- keeps content curated for onboarding instead of dumping raw docs

Cons:
- requires a small amount of backend wiring
- onboarding content still needs editorial curation

Decision: use approach 3.

## Chosen Design

### Entry Placement

Add a new onboarding button in the chat header in `internal/ui/static/index.html`.
This button lives in the same action area as the existing support button, but is visually secondary.

Visual rules:

- secondary pill style
- no status dot
- subtler background and border than the support button
- hover and active states may brighten, but default state should remain low emphasis

The support button remains the stronger call to action.

### Drawer Interaction

Clicking the onboarding button opens a right-side drawer overlay.
This drawer does not replace the chat view and does not change `currentView`.

Expected behavior:

- open from the right on desktop
- full-screen overlay on narrow/mobile layouts
- close on backdrop click
- close on `Esc`
- close with an explicit close button
- preserve the existing chat/support state when closed

### Drawer Information Architecture

The drawer is layered, not a long single-scroll document.

Tabs/sections:

1. `快速开始`
   - short explanation of what AIMA can do from this screen
   - 3-step getting-started flow
   - 5 default commands: `status`, `hardware`, `models`, `engines`, `deployments`
   - one-line explanation for each command

2. `完整命令`
   - grouped command reference for the most relevant UI-adjacent tasks
   - categories should align with existing command areas such as status, hardware, models,
     engines, deployments, fleet, and agent/direct usage

3. `遇到问题`
   - short operational fallback guidance
   - examples: agent unavailable, go to settings, use direct commands, use support flow

The drawer should feel like a guided control surface, not like a raw README.

### Command Interaction

Command examples inside the drawer must not auto-run.

Interaction rule:

- clicking a command replaces the current chat input contents with that command text and focuses the input
- the user still explicitly submits the command

This keeps the workflow safe, understandable, and aligned with the current chat input model.

## Content Source

The onboarding drawer content should be curated from:

- `docs/cli.md`
- `catalog/agent-guide.md`
- the current direct-mode help surface already present in the UI

Implementation source of truth:

- add `catalog/ui-onboarding.json` as the onboarding manifest asset
- embed it through `catalog/embed.go`
- serve it through a new route patterned after `/ui/api/support-manifest`

This keeps the UI offline-capable and avoids runtime dependence on repo-local docs files.

## Architecture Changes

### Backend

Update `internal/ui/handler.go`:

- extend `ui.Deps` with a new onboarding manifest provider
- add `GET /ui/api/onboarding-manifest`
- mirror current support manifest response behavior

Update `cmd/aima/main.go`:

- wire the onboarding manifest provider into `ui.RegisterRoutes(...)`

Add a new embedded onboarding content asset:

- use `catalog/ui-onboarding.json`
- content must include locale-aware text blocks and explicit command group definitions

### Frontend

Update `internal/ui/static/index.html`:

- add header action group support if needed
- add onboarding button styles
- add drawer overlay styles
- add Alpine state for drawer visibility, active tab, manifest data, and load status
- add manifest loading method
- add command insertion helper for the chat input
- add responsive mobile behavior

The drawer should reuse existing style language from the current glass UI and modal system
without copying the support flow interaction model.

## Error Handling and Fallbacks

- If the onboarding manifest fails to load, the button still opens the drawer.
- The drawer should show a minimal inline fallback:
  - short welcome text
  - a minimal set of default commands
  - a message that richer onboarding content is temporarily unavailable
- Failure to load onboarding content must not break chat, support, or settings.

## Testing Strategy

Automated:

- add UI route test coverage for `/ui/api/onboarding-manifest` in `internal/ui/handler_test.go`
- verify content type and response body behavior

Manual:

- desktop light theme
- desktop dark theme
- mobile layout
- Chinese and English
- open/close interactions
- `Esc` close behavior
- backdrop close behavior
- command insertion into the chat input
- support button remains visually stronger than onboarding

## Risks

- If the drawer copy is too long, it will regress into a hidden documentation dump
- If the onboarding button is too strong, it will compete with support
- If command coverage is too broad, the drawer becomes maintenance-heavy

The implementation should bias toward a compact, opinionated first-run guide.

## Implementation Readiness

This design is intentionally scoped as a UI enhancement with light route support.
It does not require changes to the agent workflow, support workflow, deployment logic,
or MCP tool implementations.
