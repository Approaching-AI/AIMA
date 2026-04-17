# Explorer Index

_Generated: 2026-04-17 07:27:08 · Agent: read-only · AIMA: regenerates each cycle_

This workspace is the Explorer's file-based memory. Read this file first in every phase.

## Mission

- Build fact-grounded exploration plans for `nvidia-gb10-arm64`
- Prefer real executable discoveries over speculative tuning
- Preserve high-signal notes about bugs, failure modes, and design doubts

## Read Order

1. `index.md`
2. `available-combos.md`
3. `device-profile.md`
4. `knowledge-base.md`
5. `experiment-facts.md`
6. `summary.md`
7. `experiments/`

## Source Of Truth

| Document | Owner | Writable | Purpose |
|----------|-------|----------|---------|
| index.md | AIMA | no | Workspace map, authority rules, required structure |
| available-combos.md | AIMA | no | Authoritative ready/blocked combo frontier |
| device-profile.md | AIMA | no | Hardware, local models, local engines, deployed state |
| knowledge-base.md | AIMA | no | History, advisories, catalog capability hints |
| experiment-facts.md | AIMA | no | Machine-generated digest of experiment outcomes and benchmark evidence |
| plan.md | Agent | yes | Scratch pad for the next executable Do phase; AIMA resets it when no Do phase is pending |
| summary.md | Agent | yes | Running memory of findings, bugs, doubts, and strategy |
| experiments/*.md | AIMA + Agent Notes | append notes only | Raw experiment outcomes |

## Hard Rules

- AIMA-generated fact documents are authoritative. If a fact is absent, treat it as unavailable.
- New tasks may only use combos listed under `## Ready Combos` in `available-combos.md`.
- Do not schedule any combo listed under `## Blocked Combos` in this round.
- Do not infer standard engines, default images, or hidden model variants from prior knowledge.
- The `query` tool supports only `search`, `compare`, `gaps`, and `aggregate`.
- Keep the required headings in `plan.md` and `summary.md` so later phases can continue from them.

## Current Fact Snapshot

_All counts below are for this exact phase; the authoritative rows are in `available-combos.md`. If you see a different Ready/Blocked count anywhere (logs, prior plan.md, explorer.status JSON), this snapshot wins._

| Metric | Value | Meaning |
|--------|-------|---------|
| Hardware Profile | nvidia-gb10-arm64 | current device |
| Local Models | 30 | models present on disk |
| Local Engines | 6 | engines installed locally |
| Ready Combos | 3 | model×engine pairs eligible for new tasks |
| Blocked Combos | 109 | pairs known to fail (format/type/VRAM/prior error) |
| Already Explored Combos | 68 | pairs with successful or failed history |
| Pending Work Items | 4 | durable obligations on ready combos |

## Required Working Doc Structure

`plan.md` should keep these sections:
- `## Objective`
- `## Fact Snapshot`
- `## Task Board`
- `## Tasks` with a YAML block

`summary.md` should keep these sections:
- `## Key Findings`
- `## Bugs And Failures`
- `## Design Doubts`
- `## Recommended Configurations` with a YAML block
- `## Confirmed Blockers` with a YAML block
- `## Do Not Retry This Cycle` with a YAML block
- `## Evidence Ledger` with a YAML block
- `## Current Strategy`
- `## Next Cycle Candidates`
