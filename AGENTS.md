# AGENTS.md

## Repository role

`es-3581100/WaterSheep` is the non-KoL experimental core fork in this lineage:

```text
SamratDuttaOfficial/WaterSheep
             ↓
      es-3581100/WaterSheep
             ↓
donCannoli-burns/KOL-WaterSheep
```

The original project is `SamratDuttaOfficial/WaterSheep`. Preserve upstream attribution.

## Domain boundary

This repository must remain free of direct **and indirect** Kingdom of Loathing / KoLmafia content.

Do not add or backport:

- KoL or KoLmafia text, examples, prompts, labels, fixtures, traces, or screenshots;
- KoL-specific schemas, routing categories, task taxonomies, or decision questions;
- synthetic data derived from KoL content;
- domain assumptions or adapters that exist specifically for KoL;
- artifacts whose model-level usefulness cannot be separated cleanly from KoL-derived content.

KoL-specific work belongs in `donCannoli-burns/KOL-WaterSheep`.

## Allowed inheritance

General, domain-neutral model work may move into this repository from upstream or downstream experiments when it is clearly separable from KoL content. Examples include model architecture changes, generic calibration methods, inference/runtime improvements, training machinery, evaluation methodology, portability work, and other demonstrably domain-neutral model-level improvements.

When provenance is ambiguous, do not import it into this core. Keep it downstream until its domain dependence can be resolved.

## Direction of travel

General WaterSheep improvements may flow downstream into KOL-WaterSheep.

KoL-specific content does not flow upstream into this repository.

The goal is to keep this fork useful as a clean non-KoL core while allowing downstream experiments to inform genuinely general model improvements.
