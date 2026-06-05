# DreamChat Image Platform Benchmark Corpus Template

## Purpose

Use this corpus to evaluate image providers, prompt templates, style profiles, consistency behavior, latency, and cost.

The same corpus should be reusable across providers.

## Evaluation Dimensions

For each generated image, score 1–5:

- visual quality
- prompt adherence
- style adherence
- character consistency
- place consistency
- artifact readability
- emotional/scene fit
- web usability as preview
- high-res final improvement
- unwanted drift/failure

Track:

- preview latency
- final latency
- estimated cost
- provider/model
- cache hit/miss
- error/failure status

## Character Prompts — 25 Slots

Use mixed genres and body/face traits.

Example structure:

```json
{
  "benchmark_id": "char_001",
  "world_genre": "political thriller",
  "character_id": "char_test_001",
  "visual_profile": {
    "apparent_age": "late 40s",
    "body_type": "stocky",
    "skin_tone": "light brown",
    "hair": "short gray curls",
    "eyes": "hazel",
    "distinctive_marks": ["small burn scar on right cheek"],
    "signature_clothing": ["dark green wool coat", "silver lapel pin"],
    "mood": "tired but alert"
  },
  "requested_pack": "character_expression_pack",
  "style_profile": "realistic cinematic noir"
}
```

Fill 25 cases:

1. char_001
2. char_002
3. char_003
4. char_004
5. char_005
6. char_006
7. char_007
8. char_008
9. char_009
10. char_010
11. char_011
12. char_012
13. char_013
14. char_014
15. char_015
16. char_016
17. char_017
18. char_018
19. char_019
20. char_020
21. char_021
22. char_022
23. char_023
24. char_024
25. char_025

## Place Prompts — 25 Slots

Example structure:

```json
{
  "benchmark_id": "place_001",
  "world_genre": "low fantasy city drama",
  "place_id": "place_test_001",
  "visual_profile": {
    "location_type": "market square",
    "architecture": "old stone arcades and narrow balconies",
    "landmarks": ["red clocktower", "large cracked fountain"],
    "palette": "warm ochre, dusty red, faded green",
    "default_mood": "busy, tense, old-world political tension"
  },
  "requested_pack": "place_minimal_scene_pack",
  "style_profile": "painterly cinematic fantasy realism"
}
```

Fill 25 cases:

1. place_001
2. place_002
3. place_003
4. place_004
5. place_005
6. place_006
7. place_007
8. place_008
9. place_009
10. place_010
11. place_011
12. place_012
13. place_013
14. place_014
15. place_015
16. place_016
17. place_017
18. place_018
19. place_019
20. place_020
21. place_021
22. place_022
23. place_023
24. place_024
25. place_025

## Artifact Prompts — 25 Slots

Example structure:

```json
{
  "benchmark_id": "artifact_001",
  "world_genre": "modern conspiracy thriller",
  "artifact_id": "artifact_test_001",
  "visual_profile": {
    "artifact_type": "warrant document",
    "material": "creased official paper",
    "key_visual_features": ["red official stamp", "blurred typed names", "fold marks"],
    "mood": "bureaucratic threat"
  },
  "asset_role": "document_preview",
  "style_profile": "realistic cinematic"
}
```

Fill 25 cases:

1. artifact_001
2. artifact_002
3. artifact_003
4. artifact_004
5. artifact_005
6. artifact_006
7. artifact_007
8. artifact_008
9. artifact_009
10. artifact_010
11. artifact_011
12. artifact_012
13. artifact_013
14. artifact_014
15. artifact_015
16. artifact_016
17. artifact_017
18. artifact_018
19. artifact_019
20. artifact_020
21. artifact_021
22. artifact_022
23. artifact_023
24. artifact_024
25. artifact_025

## Hard Cross-Genre Prompts — 25 Slots

Use difficult cases:

- same character across different expressions
- same place across day/night/weather
- mature dark setting but not overdone
- realistic non-fantasy scenes
- sci-fi technical spaces
- horror mood without losing place identity
- political/office scenes
- companion-like domestic scenes
- multiple cultural/visual styles
- artifact with approximate text-like layout

Fill 25 cases:

1. hard_001
2. hard_002
3. hard_003
4. hard_004
5. hard_005
6. hard_006
7. hard_007
8. hard_008
9. hard_009
10. hard_010
11. hard_011
12. hard_012
13. hard_013
14. hard_014
15. hard_015
16. hard_016
17. hard_017
18. hard_018
19. hard_019
20. hard_020
21. hard_021
22. hard_022
23. hard_023
24. hard_024
25. hard_025

## Benchmark Output Table

| benchmark_id | provider | model | preview_latency_ms | final_latency_ms | estimated_cost | quality | consistency | style_adherence | prompt_adherence | notes |
|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---|
| char_001 | | | | | | | | | | |

---

## Confidence to Implement

**Score: 60/100 — Medium**

The *runner* is easy: a script that iterates a JSON corpus, calls the platform API, records latency/cost/asset_ids, and writes a results CSV. That part is ~95/100. The hard parts are everything around it:

1. **Filling the 100 prompt slots with representative cases** is a creative product task, not engineering — without real prompts the template is a shell.
2. **Scoring quality / consistency / style / prompt adherence on a 1–5 scale** requires either human raters (expensive, slow) or an automated judge (CLIP similarity, LLM grading, face/landmark embedding similarity), each with its own implementation cost and calibration drift.
3. The "consistency" score in particular needs a paired-image comparison framework (same character across expressions, same place across day/night) which isn't specified.

I'm averaging to **60** because the deliverable as written is genuinely under-specified for "implement end-to-end." A first pass can ship the runner + an LLM-judge stub and defer human eval.

