## Context

The Details pane already inspects the highlighted container; Findings already streams an
image's archive through `forensics.Analyse` as a job and caches the report by image and
platform. Space already maps history onto layers only when the mapping cannot be wrong.

## Goals / Non-Goals

Goals: what a re-deploy would bring, from cheap reads first, honest about every unknown.

Non-goals: asking a registry; file-level differences; comparing environment values; any
action that re-creates or updates a container; a severity score.

## Decisions

Recorded in full in `docs/DECISIONS.md`, round eight (C1–C12). In short: the reference comes
from `Config.Image`; `ImageFacts` drops environment values where the answer is decoded; the
lineage is the common prefix of diff IDs; content comparison reuses Findings' analysis and
cache; findings match by rule, path and evidence (layer findings by instruction); refusals are
checked from the cheapest evidence up.

## Risks / Trade-offs

- A value changed under the same variable name is invisible by construction; the tab says so.
- An image whose history does not map onto its layers shows layers without instructions.
- Comparing reads two archives; it happens only on Enter, and a cached report is not read again.
