# DreamChat Image Platform — Benchmark Corpus v0.1

> **Status**: this file replaces the earlier `benchmark_corpus_template.md`
> placeholder. It is a real, runnable corpus with 100 cases (25 characters,
> 25 places, 25 artifacts, 25 consistency stress tests), an explicit
> scoring rubric, operational pass/fail checks, and a scoring policy.

## 1. Purpose

The benchmark corpus is the platform's standard way to compare providers,
models, prompt templates, style profiles, and consistency behavior. It
runs **before** a provider is promoted to production routing
(`production_capable` per PRD 03 §8) and **after** any router or
prompt-compiler change to detect regressions.

Inputs:

- 100 benchmark cases (`§5`–`§8` below).
- One or more provider+model pairs to evaluate.
- A scoring rubric (`§3`) and policy (`§4`).

Outputs:

- One results row per (benchmark_id × provider × model) with quality,
  latency, cost, consistency, and pass/fail.

## 2. How to run

1. Pick a candidate provider model (e.g. `bfl/flux-2-klein`).
2. For each benchmark case in §§5–8:
   - POST the case to the relevant generation endpoint
     (`POST /v1/characters/{id}/generate-pack`,
     `POST /v1/places/{id}/generate-pack`,
     `POST /v1/artifacts/{id}/generate`).
   - For consistency tests (§8), generate the anchor first, then each
     variant referencing the anchor.
   - Poll job status until terminal.
   - Record operational fields per `§3.2` and the generated asset IDs.
3. A reviewer scores each output on the dimensions listed for the case
   (`§3.1`).
4. Compute per-(provider, model) aggregates and apply pass/fail per `§4`.
5. File the run as a `benchmark_run` (table TBD) referencing all results.

LLM/image-judge evaluation may augment human scoring later (`§4` policy)
but must be marked experimental.

## 3. Scoring rubric

### 3.1 Quality dimensions (1–5 scale)

| Dimension | What 1 looks like | What 5 looks like |
|---|---|---|
| `prompt_adherence` | Image ignores or contradicts the prompt. | Every key visual attribute in the prompt is present and recognizable. |
| `identity_consistency` | Subject in variant is unrecognizable as the anchor. | Subject is clearly the same person across all variants. |
| `place_consistency` | Variant looks like a different location. | Same architecture, landmarks, palette identity carry across variants. |
| `artifact_recognizability` | Object is generic; type or function unclear. | Specific object as described, with the requested state legible. |
| `style_adherence` | Style is generic or contradictory. | Matches the named style profile (lighting, palette, composition) closely. |
| `composition_quality` | Bad framing, awkward crops, broken anatomy. | Strong framing, balanced composition, no anatomy/perspective issues. |
| `web_app_usability` | Cannot be used in the UI without rework. | Drops into the scene canvas / participant panel / sidebar cleanly. |
| `low_res_readability` | Subject illegible at thumbnail size (256 px). | Subject still recognizable as a 256 px thumbnail. |
| `generation_artifacts` | Visible model artifacts (hands, eyes, text, melting). | No model artifacts visible. |
| `mature_policy_compatibility` (where relevant) | Output is refused, censored, or sanitized away from the brief. | Output respects the brief within platform policy. |

Score every dimension listed in the case's `evaluation_dimensions`. Skip
dimensions not listed (e.g. `place_consistency` doesn't apply to a single
artifact case).

### 3.2 Operational pass/fail checks

Every benchmark result must record these. Any missing field is a fail for
that result regardless of human scoring.

| Check | Pass condition |
|---|---|
| Preview latency present | `preview_latency_ms` recorded and > 0. |
| Final latency present | `final_latency_ms` recorded (or N/A if `generate_final=false`). |
| Estimated cost present | `estimated_cost_usd` recorded. |
| Actual cost present | `actual_cost_usd` recorded after job completes (or N/A if provider doesn't report). |
| Provider metadata present | `provider_id`, `model_id` recorded on the asset. |
| Model metadata present | Model identifier matches the candidate under test. |
| Seed / reference metadata present | `seed` recorded when provider supports seeds; `reference_asset_ids` recorded when reference conditioning is used. |
| Asset stored successfully | `visual_assets` row created with `status` ∈ {preview_ready, ready}. |
| Low-res URL present | `low_res_url` not null. |
| High-res URL present (if requested) | `high_res_url` not null when `generate_final=true`. |

## 4. Scoring policy

- **Human review is the first scoring method.** Two reviewers minimum
  for any candidate evaluated for production promotion; one reviewer for
  experimental / development scoring.
- **LLM / image-judge evaluation may be added later** (CLIP similarity,
  face-embedding similarity, vision-LLM grading) but must be marked
  `experimental` in the results row and may not by itself promote a
  candidate.
- **Identity consistency floor** — for consistency-critical character
  assets (any case with `evaluation_dimensions` including
  `identity_consistency`), a score < 4/5 on **any** variant is a hard
  fail for the run.
- **Place consistency floor** — for recurring place assets (any case
  with `place_consistency`), a score < 4/5 on any variant is a hard
  fail.
- **Web-app preview floor** — for any case where the asset is intended
  for the web app's scene canvas or participants panel, `low_res_readability`
  < 3/5 is a hard fail.
- **Capability mapping** — a candidate that fails identity-consistency
  cases must not be classified above `scene_capable`; a candidate that
  fails place-consistency cases must not be classified above
  `draft_only` for place generation; see PRD 03 §8.3.

## 5. Character prompts (25)

Each case targets a character pack or single portrait. `generation_mode`
is `pack` (multi-variant) or `single`. `required_capability` is
`identity_capable` or higher per PRD 03 §8.

```json
{ "benchmark_id":"char_001","asset_type":"character_portrait","generation_mode":"pack",
  "prompt":"Detective Ana Vass, late 30s, olive skin, short dark wavy hair, tired hazel eyes, faint diagonal scar across right brow. Wears a worn navy peacoat over a thin gray sweater, brass buttons. Weary shoulders. Late-shift focus.",
  "style_profile":"realistic cinematic noir",
  "required_capability":"identity_capable",
  "expected_outputs":["neutral_front","neutral_3q","warm","serious","tense"],
  "evaluation_dimensions":["prompt_adherence","identity_consistency","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["identity_consistency<4 on any variant","prompt_adherence<3","missing scar across right brow on any variant"] }
```

```json
{ "benchmark_id":"char_002","asset_type":"character_portrait","generation_mode":"pack",
  "prompt":"Dr. Yusuf Karim, mid 60s, dark brown skin, close-cropped silver beard, deep-set brown eyes, half-rim reading glasses. Wears a crisp white coat over a charcoal vest and burgundy tie. Composed, scholarly bearing.",
  "style_profile":"realistic cinematic clean",
  "required_capability":"identity_capable",
  "expected_outputs":["neutral_front","neutral_3q","warm","serious","concerned"],
  "evaluation_dimensions":["prompt_adherence","identity_consistency","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["identity_consistency<4 on any variant","missing glasses on any variant"] }
```

```json
{ "benchmark_id":"char_003","asset_type":"character_portrait","generation_mode":"pack",
  "prompt":"Marisol Vega, 31, Latina, sharp pixie cut, alert green eyes, single small mole left cheek. Navy pencil skirt, white silk blouse, ID lanyard. Composed, watchful — political aide on a long day.",
  "style_profile":"realistic cinematic noir",
  "required_capability":"identity_capable",
  "expected_outputs":["neutral_front","neutral_3q","warm","serious","tense"],
  "evaluation_dimensions":["prompt_adherence","identity_consistency","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["identity_consistency<4 on any variant","missing mole on any variant"] }
```

```json
{ "benchmark_id":"char_004","asset_type":"character_portrait","generation_mode":"pack",
  "prompt":"Kjell Mortensen, 54, weathered Nordic harbor worker, salt-and-pepper short beard, lined face, faded blue tattoo of an anchor on left forearm. Yellow oilskin jacket over heavy wool sweater, knit cap.",
  "style_profile":"realistic cinematic muted",
  "required_capability":"identity_capable",
  "expected_outputs":["neutral_front","neutral_3q","warm","serious","tired"],
  "evaluation_dimensions":["prompt_adherence","identity_consistency","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["identity_consistency<4 on any variant","missing anchor tattoo where visible"] }
```

```json
{ "benchmark_id":"char_005","asset_type":"character_portrait","generation_mode":"pack",
  "prompt":"Mei, 16, Chinese-American teenage runaway, sleep-deprived eyes, dark hair in a messy half-bun, thin frame. Oversized army-surplus jacket, hand-stitched patches, one strap of a worn backpack. Defensive, alert.",
  "style_profile":"realistic cinematic grounded",
  "required_capability":"identity_capable",
  "expected_outputs":["neutral_front","neutral_3q","serious","tense","afraid"],
  "evaluation_dimensions":["prompt_adherence","identity_consistency","style_adherence","composition_quality","web_app_usability","low_res_readability","mature_policy_compatibility"],
  "failure_conditions":["identity_consistency<4 on any variant","character reads as adult"] }
```

```json
{ "benchmark_id":"char_006","asset_type":"character_portrait","generation_mode":"pack",
  "prompt":"Eitha the Ranger, 30s, lean and muscular, short auburn hair, freckled tan skin, sea-grey eyes, faint tattoo of three pine needles below left collarbone. Worn leather jerkin over dark green wool, hood half-up, longbow strap across chest.",
  "style_profile":"painterly cinematic fantasy realism",
  "required_capability":"identity_capable",
  "expected_outputs":["neutral_front","neutral_3q","warm","serious","tense"],
  "evaluation_dimensions":["prompt_adherence","identity_consistency","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["identity_consistency<4 on any variant","missing pine-needle tattoo where visible"] }
```

```json
{ "benchmark_id":"char_007","asset_type":"character_portrait","generation_mode":"pack",
  "prompt":"Archmage Selen Vorr, 70s, pale skin, long silver hair gathered low, sharp gray eyes, ink-stained fingertips. Layered indigo robes with silver star embroidery at the cuffs, narrow oak staff with a dim blue stone.",
  "style_profile":"painterly cinematic fantasy realism",
  "required_capability":"identity_capable",
  "expected_outputs":["neutral_front","neutral_3q","warm","serious","commanding"],
  "evaluation_dimensions":["prompt_adherence","identity_consistency","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["identity_consistency<4 on any variant"] }
```

```json
{ "benchmark_id":"char_008","asset_type":"character_portrait","generation_mode":"pack",
  "prompt":"Wickit, a goblin street merchant, knee-high, leathery green skin, large amber eyes, one notched ear. Patchwork coat in mustard and red, brass buttons, leather pouches at belt. Sly, half-smiling.",
  "style_profile":"painterly cinematic fantasy realism",
  "required_capability":"identity_capable",
  "expected_outputs":["neutral_front","neutral_3q","warm","sly","alarmed"],
  "evaluation_dimensions":["prompt_adherence","identity_consistency","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["identity_consistency<4 on any variant","missing notched ear on any variant"] }
```

```json
{ "benchmark_id":"char_009","asset_type":"character_portrait","generation_mode":"pack",
  "prompt":"Sir Halvard, 24, paladin, broad shoulders, short fair hair, pale blue eyes, clean-shaven, sun-burned cheeks. Polished steel breastplate over dark blue gambeson, white wolf sigil at chest, sword hilt at hip.",
  "style_profile":"painterly cinematic fantasy realism",
  "required_capability":"identity_capable",
  "expected_outputs":["neutral_front","neutral_3q","resolute","grim","wounded"],
  "evaluation_dimensions":["prompt_adherence","identity_consistency","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["identity_consistency<4 on any variant","missing wolf sigil on any variant"] }
```

```json
{ "benchmark_id":"char_010","asset_type":"character_portrait","generation_mode":"pack",
  "prompt":"Lirien the Dryad, otherworldly humanoid, bark-textured skin in soft brown, eyes the color of wet moss, hair of slim ivy vines. Wears a wrap of woven leaves and bark, faint glowing flowers at the temples.",
  "style_profile":"painterly cinematic fantasy realism",
  "required_capability":"identity_capable",
  "expected_outputs":["neutral_front","neutral_3q","serene","wary","mournful"],
  "evaluation_dimensions":["prompt_adherence","identity_consistency","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["identity_consistency<4 on any variant","reads as ordinary human on any variant"] }
```

```json
{ "benchmark_id":"char_011","asset_type":"character_portrait","generation_mode":"pack",
  "prompt":"Rax-7, cybernetic mercenary, 30s, dark skin, shaved head, chromed left arm with visible servos at the shoulder, glowing amber optical implant over right eye. Black armored jacket, ammo straps across chest.",
  "style_profile":"sci-fi cinematic gritty",
  "required_capability":"identity_capable",
  "expected_outputs":["neutral_front","neutral_3q","serious","tense","intimidating"],
  "evaluation_dimensions":["prompt_adherence","identity_consistency","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["identity_consistency<4 on any variant","missing chromed arm or amber implant on any variant"] }
```

```json
{ "benchmark_id":"char_012","asset_type":"character_portrait","generation_mode":"pack",
  "prompt":"Administrator Inara Vey, early 40s, South Asian, dark hair in a tight low bun, dark brown eyes, neutral expression. Crisp slate-gray jumpsuit with silver stationmaster insignia at collar, tablet under arm.",
  "style_profile":"sci-fi cinematic clean",
  "required_capability":"identity_capable",
  "expected_outputs":["neutral_front","neutral_3q","warm","serious","commanding"],
  "evaluation_dimensions":["prompt_adherence","identity_consistency","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["identity_consistency<4 on any variant","missing collar insignia"] }
```

```json
{ "benchmark_id":"char_013","asset_type":"character_portrait","generation_mode":"pack",
  "prompt":"Dr. Calix Onuh, 52, xenobiologist, dark skin, salt-pepper short locs, smile lines, intricate spiraling tattoo curling up the right side of the neck. White lab coat over teal scrubs, sample vial in breast pocket.",
  "style_profile":"sci-fi cinematic clean",
  "required_capability":"identity_capable",
  "expected_outputs":["neutral_front","neutral_3q","warm","curious","alarmed"],
  "evaluation_dimensions":["prompt_adherence","identity_consistency","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["identity_consistency<4 on any variant","missing neck tattoo where visible"] }
```

```json
{ "benchmark_id":"char_014","asset_type":"character_portrait","generation_mode":"pack",
  "prompt":"Lieutenant Sora Kade, 28, mixed-Asian pilot, almond eyes, short black undercut, faded burn scar along left jawline. Charcoal flight jumpsuit, gloves clipped at belt, helmet under arm.",
  "style_profile":"sci-fi cinematic clean",
  "required_capability":"identity_capable",
  "expected_outputs":["neutral_front","neutral_3q","warm","focused","grim"],
  "evaluation_dimensions":["prompt_adherence","identity_consistency","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["identity_consistency<4 on any variant","missing jaw scar on any variant"] }
```

```json
{ "benchmark_id":"char_015","asset_type":"character_portrait","generation_mode":"pack",
  "prompt":"Lord Avenir, 60s, exiled noble in a sci-fi setting, gaunt, sharp cheekbones, long graying hair tied back, faded family signet ring on right hand. Once-fine velvet coat in deep wine, frayed at the cuffs.",
  "style_profile":"sci-fi cinematic painterly",
  "required_capability":"identity_capable",
  "expected_outputs":["neutral_front","neutral_3q","cold","sorrowful","manic"],
  "evaluation_dimensions":["prompt_adherence","identity_consistency","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["identity_consistency<4 on any variant","missing signet ring where visible"] }
```

```json
{ "benchmark_id":"char_016","asset_type":"character_portrait","generation_mode":"pack",
  "prompt":"Father Aaron Idris, 40s, occult investigator, hollow eyes ringed dark, brown hair flecked early gray, three days of stubble. Black wool coat over rumpled collared shirt, no clerical collar visible.",
  "style_profile":"horror cinematic muted",
  "required_capability":"identity_capable",
  "expected_outputs":["neutral_front","neutral_3q","wary","terrified","haunted"],
  "evaluation_dimensions":["prompt_adherence","identity_consistency","style_adherence","composition_quality","web_app_usability","low_res_readability","mature_policy_compatibility"],
  "failure_conditions":["identity_consistency<4 on any variant"] }
```

```json
{ "benchmark_id":"char_017","asset_type":"character_portrait","generation_mode":"pack",
  "prompt":"Padre Esteban, 60s, weathered village priest, deep-set brown eyes, gray hair receding, heavy crucifix on a worn cord. Black cassock with a fresh dark stain across the front, hands marked with old scars.",
  "style_profile":"horror cinematic muted",
  "required_capability":"identity_capable",
  "expected_outputs":["neutral_front","neutral_3q","grieved","resolute","afraid"],
  "evaluation_dimensions":["prompt_adherence","identity_consistency","style_adherence","composition_quality","web_app_usability","low_res_readability","mature_policy_compatibility"],
  "failure_conditions":["identity_consistency<4 on any variant","missing crucifix on any variant"] }
```

```json
{ "benchmark_id":"char_018","asset_type":"character_portrait","generation_mode":"pack",
  "prompt":"Liana, 24, cult survivor, hollow cheeks, shorn-on-one-side dark hair, ritual scar in the shape of a triangle on the right collarbone. Threadbare gray dress, mismatched military boots, blanket around shoulders.",
  "style_profile":"horror cinematic muted",
  "required_capability":"identity_capable",
  "expected_outputs":["neutral_front","neutral_3q","wary","manic","exhausted"],
  "evaluation_dimensions":["prompt_adherence","identity_consistency","style_adherence","composition_quality","web_app_usability","low_res_readability","mature_policy_compatibility"],
  "failure_conditions":["identity_consistency<4 on any variant","missing triangular collarbone scar on any variant"] }
```

```json
{ "benchmark_id":"char_019","asset_type":"character_portrait","generation_mode":"pack",
  "prompt":"Elena Voronova, 32, romantic-drama lead, classical features, long dark hair in soft waves, dark brown eyes, small beauty mark above the lip. Wears a cream silk blouse, single strand of pearls.",
  "style_profile":"romantic cinematic soft",
  "required_capability":"identity_capable",
  "expected_outputs":["neutral_front","neutral_3q","warm","tender","tearful"],
  "evaluation_dimensions":["prompt_adherence","identity_consistency","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["identity_consistency<4 on any variant","missing beauty mark on any variant"] }
```

```json
{ "benchmark_id":"char_020","asset_type":"character_portrait","generation_mode":"pack",
  "prompt":"Tomás Reyes, 56, widower, salt-pepper hair, neatly trimmed beard, warm brown eyes carrying grief. Worn cardigan over a faded button-down, wedding ring still on left hand.",
  "style_profile":"romantic cinematic soft",
  "required_capability":"identity_capable",
  "expected_outputs":["neutral_front","neutral_3q","warm","mournful","resolute"],
  "evaluation_dimensions":["prompt_adherence","identity_consistency","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["identity_consistency<4 on any variant","missing wedding ring on any variant"] }
```

```json
{ "benchmark_id":"char_021","asset_type":"character_portrait","generation_mode":"pack",
  "prompt":"Daniel Lang, 38, ordinary-looking spy — the kind nobody describes well. Average height, brown hair, hazel eyes, plain features, no distinctive marks. Cheap gray jacket over a plain navy shirt. Forgettable.",
  "style_profile":"realistic cinematic noir",
  "required_capability":"identity_capable",
  "expected_outputs":["neutral_front","neutral_3q","warm","serious","tense"],
  "evaluation_dimensions":["prompt_adherence","identity_consistency","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["identity_consistency<4 on any variant — the test here is whether identity holds for an intentionally bland brief"] }
```

```json
{ "benchmark_id":"char_022","asset_type":"character_portrait","generation_mode":"pack",
  "prompt":"Aisha Bello, 43, investigative journalist, dark skin, natural hair pulled back, sharp eyes behind tortoiseshell glasses. Charcoal blazer over plum blouse, press lanyard tucked into pocket.",
  "style_profile":"realistic cinematic clean",
  "required_capability":"identity_capable",
  "expected_outputs":["neutral_front","neutral_3q","warm","serious","alarmed"],
  "evaluation_dimensions":["prompt_adherence","identity_consistency","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["identity_consistency<4 on any variant","missing tortoiseshell glasses on any variant"] }
```

```json
{ "benchmark_id":"char_023","asset_type":"character_portrait","generation_mode":"pack",
  "prompt":"Nonna Catarina, 82, Sicilian grandmother, deeply lined warm face, soft white hair pinned in a low bun, gold cross at neck. Floral house-dress, knitted shawl over shoulders, gentle hands.",
  "style_profile":"realistic cinematic warm",
  "required_capability":"identity_capable",
  "expected_outputs":["neutral_front","neutral_3q","warm","serious","mischievous"],
  "evaluation_dimensions":["prompt_adherence","identity_consistency","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["identity_consistency<4 on any variant","age reads younger than late 70s on any variant"] }
```

```json
{ "benchmark_id":"char_024","asset_type":"character_portrait","generation_mode":"pack",
  "prompt":"Captain Naima Okeke, 35, athletic upper body, in a sleek titanium-framed manual wheelchair. Close-cropped black hair, deep brown skin, faint tribal scarification at temples. Tactical jacket, gloves, holster at thigh.",
  "style_profile":"sci-fi cinematic clean",
  "required_capability":"identity_capable",
  "expected_outputs":["neutral_front","neutral_3q","warm","serious","commanding"],
  "evaluation_dimensions":["prompt_adherence","identity_consistency","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["identity_consistency<4 on any variant","wheelchair absent or mis-rendered on any variant","scarification missing where visible"] }
```

```json
{ "benchmark_id":"char_025","asset_type":"character_portrait","generation_mode":"pack",
  "prompt":"Henrik Vance, 47, detective, square jaw, short ash-blond hair, prominent prosthetic eye in the left socket — visible mechanical iris ringed in steel. Brown leather jacket over a gray turtleneck.",
  "style_profile":"sci-fi cinematic noir",
  "required_capability":"identity_capable",
  "expected_outputs":["neutral_front","neutral_3q","warm","serious","cold"],
  "evaluation_dimensions":["prompt_adherence","identity_consistency","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["identity_consistency<4 on any variant","prosthetic eye absent or mis-rendered on any variant","prosthetic on wrong side on any variant"] }
```

## 6. Place prompts (25)

Each case targets a place pack or single scene. `required_capability` is
typically `scene_capable` or higher.

```json
{ "benchmark_id":"place_001","asset_type":"place_scene","generation_mode":"single",
  "prompt":"Modern police interrogation room. Bare concrete walls, single steel table bolted to floor, two mismatched chairs, harsh overhead fluorescent. One-way mirror on the back wall, faint marks of past occupants.",
  "style_profile":"realistic cinematic noir",
  "required_capability":"scene_capable",
  "expected_outputs":["establishing_wide"],
  "evaluation_dimensions":["prompt_adherence","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["prompt_adherence<3","fluorescent reads as warm light"] }
```

```json
{ "benchmark_id":"place_002","asset_type":"place_scene","generation_mode":"single",
  "prompt":"Senator's private office. Heavy mahogany desk, leather chairs, oil portraits of past senators on the wall, brass lamp casting warm light, US flag and state flag flanking the desk. Afternoon sun through wooden blinds.",
  "style_profile":"realistic cinematic warm",
  "required_capability":"scene_capable",
  "expected_outputs":["establishing_wide"],
  "evaluation_dimensions":["prompt_adherence","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["prompt_adherence<3"] }
```

```json
{ "benchmark_id":"place_003","asset_type":"place_scene","generation_mode":"single",
  "prompt":"Small apartment kitchen at dawn. Worn wooden cabinets, half-empty coffee pot, mug with steam, single plate in sink, morning light through gauzy curtain. Lived-in, slightly cluttered, intimate.",
  "style_profile":"realistic cinematic warm",
  "required_capability":"scene_capable",
  "expected_outputs":["establishing_wide","closer_atmospheric"],
  "evaluation_dimensions":["prompt_adherence","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["prompt_adherence<3","kitchen reads as commercial"] }
```

```json
{ "benchmark_id":"place_004","asset_type":"place_scene","generation_mode":"single",
  "prompt":"Subway platform at 2 AM. Sodium-yellow lights, graffiti on tiles, single newspaper on a bench, empty tracks below, departure board flickering. Sense of just-missed-train solitude.",
  "style_profile":"realistic cinematic noir",
  "required_capability":"scene_capable",
  "expected_outputs":["establishing_wide"],
  "evaluation_dimensions":["prompt_adherence","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["prompt_adherence<3","scene reads as crowded"] }
```

```json
{ "benchmark_id":"place_005","asset_type":"place_scene","generation_mode":"single",
  "prompt":"Hospital ward, daytime, soft sterile lighting. Two rows of empty beds with neat blue curtains drawn back, IV stands, a window at the end with a tree visible. Clean but not cold.",
  "style_profile":"realistic cinematic clean",
  "required_capability":"scene_capable",
  "expected_outputs":["establishing_wide"],
  "evaluation_dimensions":["prompt_adherence","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["prompt_adherence<3","beds occupied with patients (privacy)"] }
```

```json
{ "benchmark_id":"place_006","asset_type":"place_scene","generation_mode":"single",
  "prompt":"Open-air street market, midday, vibrant. Fabric awnings in red and yellow, fruit stalls with oranges and pomegranates, brass scales, vendors and shoppers from multiple ethnicities, cobblestones underfoot.",
  "style_profile":"realistic cinematic warm",
  "required_capability":"scene_capable",
  "expected_outputs":["establishing_wide","closer_atmospheric"],
  "evaluation_dimensions":["prompt_adherence","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["prompt_adherence<3","scene reads as deserted"] }
```

```json
{ "benchmark_id":"place_007","asset_type":"place_scene","generation_mode":"single",
  "prompt":"Rooftop bar at dusk, urban skyline. String lights, low tables with candles, a few patrons, a bartender wiping a glass at a marble-topped bar. Magic-hour purple-orange sky behind.",
  "style_profile":"realistic cinematic warm",
  "required_capability":"scene_capable",
  "expected_outputs":["establishing_wide"],
  "evaluation_dimensions":["prompt_adherence","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["prompt_adherence<3","time of day not dusk"] }
```

```json
{ "benchmark_id":"place_008","asset_type":"place_scene","generation_mode":"single",
  "prompt":"Empty coastal highway at noon, cliffs to one side, ocean to the other. Asphalt shimmering with heat, a single rusted speed-limit sign, no cars, gulls overhead.",
  "style_profile":"realistic cinematic cinematic-bright",
  "required_capability":"scene_capable",
  "expected_outputs":["establishing_wide"],
  "evaluation_dimensions":["prompt_adherence","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["prompt_adherence<3","cars present in scene"] }
```

```json
{ "benchmark_id":"place_009","asset_type":"place_scene","generation_mode":"pack",
  "prompt":"Fantasy throne hall, twilight. Long aisle, dark stone pillars, gilded throne on a raised dais, banners with a silver crescent on midnight blue, narrow stained-glass windows admitting deep blue light.",
  "style_profile":"painterly cinematic fantasy realism",
  "required_capability":"scene_capable",
  "expected_outputs":["establishing_wide","closer_atmospheric","day_view","night_view"],
  "evaluation_dimensions":["prompt_adherence","place_consistency","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["prompt_adherence<3","place_consistency<4 across pack","crescent sigil missing or recolored"] }
```

```json
{ "benchmark_id":"place_010","asset_type":"place_scene","generation_mode":"single",
  "prompt":"Stone temple interior at dawn. Tall vaulted ceilings, columns of pale limestone, a circular skylight admitting a single beam of golden light onto a worn altar of green marble. Quiet, reverent.",
  "style_profile":"painterly cinematic fantasy realism",
  "required_capability":"scene_capable",
  "expected_outputs":["establishing_wide"],
  "evaluation_dimensions":["prompt_adherence","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["prompt_adherence<3"] }
```

```json
{ "benchmark_id":"place_011","asset_type":"place_scene","generation_mode":"single",
  "prompt":"Fantasy village square during a harvest festival. Lanterns strung between half-timbered houses, central fountain, vendors with carts of bread and woven goods, children running, late afternoon golden light.",
  "style_profile":"painterly cinematic fantasy realism",
  "required_capability":"scene_capable",
  "expected_outputs":["establishing_wide","closer_atmospheric"],
  "evaluation_dimensions":["prompt_adherence","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["prompt_adherence<3","scene reads as deserted"] }
```

```json
{ "benchmark_id":"place_012","asset_type":"place_scene","generation_mode":"single",
  "prompt":"Haunted forest at night, moonlight filtering through dense fog. Gnarled black-barked trees, deep moss underfoot, pale fungus glowing faintly, narrow path disappearing into the murk. Dread.",
  "style_profile":"horror cinematic muted",
  "required_capability":"scene_capable",
  "expected_outputs":["establishing_wide","closer_atmospheric"],
  "evaluation_dimensions":["prompt_adherence","style_adherence","composition_quality","web_app_usability","low_res_readability","mature_policy_compatibility"],
  "failure_conditions":["prompt_adherence<3","scene reads as daytime"] }
```

```json
{ "benchmark_id":"place_013","asset_type":"place_scene","generation_mode":"pack",
  "prompt":"Sci-fi command bridge of a midsize cruiser. Curved consoles in matte black with cyan trim, holographic tactical display floating above central table, viewport showing distant blue planet, crew stations occupied.",
  "style_profile":"sci-fi cinematic clean",
  "required_capability":"scene_capable",
  "expected_outputs":["establishing_wide","closer_atmospheric","calm_empty","busy_active"],
  "evaluation_dimensions":["prompt_adherence","place_consistency","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["prompt_adherence<3","place_consistency<4 across pack"] }
```

```json
{ "benchmark_id":"place_014","asset_type":"place_scene","generation_mode":"single",
  "prompt":"Cargo bay, sci-fi freighter, low industrial lighting. Stacked olive-drab containers strapped with orange webbing, exposed pipes and conduits overhead, a sleeping cat curled on top of one crate.",
  "style_profile":"sci-fi cinematic gritty",
  "required_capability":"scene_capable",
  "expected_outputs":["establishing_wide"],
  "evaluation_dimensions":["prompt_adherence","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["prompt_adherence<3","missing cat"] }
```

```json
{ "benchmark_id":"place_015","asset_type":"place_scene","generation_mode":"single",
  "prompt":"Underground bunker, emergency lighting (red and amber). Bare concrete, blast door at one end, banks of analog instrumentation, a folded cot, a chalk tally on the wall.",
  "style_profile":"sci-fi cinematic noir",
  "required_capability":"scene_capable",
  "expected_outputs":["establishing_wide"],
  "evaluation_dimensions":["prompt_adherence","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["prompt_adherence<3"] }
```

```json
{ "benchmark_id":"place_016","asset_type":"place_scene","generation_mode":"single",
  "prompt":"Spaceport exterior at dusk on a desert world. Twin suns on the horizon, dust-streaked landing pads, a small freighter spooling its engines, low warehouses, distant red mesas.",
  "style_profile":"sci-fi cinematic painterly",
  "required_capability":"scene_capable",
  "expected_outputs":["establishing_wide"],
  "evaluation_dimensions":["prompt_adherence","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["prompt_adherence<3","missing twin suns"] }
```

```json
{ "benchmark_id":"place_017","asset_type":"place_scene","generation_mode":"single",
  "prompt":"Abandoned asylum, interior corridor at night, peeling paint, overturned wheelchair, faint moonlight through broken windows. No figures present. Dread without gore.",
  "style_profile":"horror cinematic muted",
  "required_capability":"scene_capable",
  "expected_outputs":["establishing_wide"],
  "evaluation_dimensions":["prompt_adherence","style_adherence","composition_quality","web_app_usability","low_res_readability","mature_policy_compatibility"],
  "failure_conditions":["prompt_adherence<3","explicit gore present"] }
```

```json
{ "benchmark_id":"place_018","asset_type":"place_scene","generation_mode":"single",
  "prompt":"Foggy cemetery at night, weathered stone markers in irregular rows, leafless oak in the background, a wrought iron gate slightly ajar. Quiet, oppressive atmosphere.",
  "style_profile":"horror cinematic muted",
  "required_capability":"scene_capable",
  "expected_outputs":["establishing_wide","closer_atmospheric"],
  "evaluation_dimensions":["prompt_adherence","style_adherence","composition_quality","web_app_usability","low_res_readability","mature_policy_compatibility"],
  "failure_conditions":["prompt_adherence<3","reads as bright daytime"] }
```

```json
{ "benchmark_id":"place_019","asset_type":"place_scene","generation_mode":"single",
  "prompt":"Town square the day after a bombing. Rubble, a collapsed clocktower with the face still partly intact, scorched stone, a child's red shoe in the dust, no figures present. Gray overcast light.",
  "style_profile":"realistic cinematic muted",
  "required_capability":"scene_capable",
  "expected_outputs":["establishing_wide"],
  "evaluation_dimensions":["prompt_adherence","style_adherence","composition_quality","web_app_usability","low_res_readability","mature_policy_compatibility"],
  "failure_conditions":["prompt_adherence<3","explicit casualties depicted"] }
```

```json
{ "benchmark_id":"place_020","asset_type":"place_scene","generation_mode":"single",
  "prompt":"Festival street at night, packed with people, paper lanterns above, food stalls steaming, glowing signs in mixed scripts, neon reflection in shallow puddles. Joyful, dense, vibrant.",
  "style_profile":"realistic cinematic vibrant",
  "required_capability":"scene_capable",
  "expected_outputs":["establishing_wide","closer_atmospheric"],
  "evaluation_dimensions":["prompt_adherence","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["prompt_adherence<3","scene reads as empty"] }
```

```json
{ "benchmark_id":"place_021","asset_type":"place_scene","generation_mode":"single",
  "prompt":"Same festival street at dawn after the crowd leaves. Crushed paper lanterns and confetti on wet pavement, a single street sweeper in the distance, gray pre-sunrise light, stalls closed.",
  "style_profile":"realistic cinematic muted",
  "required_capability":"scene_capable",
  "expected_outputs":["establishing_wide"],
  "evaluation_dimensions":["prompt_adherence","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["prompt_adherence<3","scene reads as nighttime"] }
```

```json
{ "benchmark_id":"place_022","asset_type":"place_scene","generation_mode":"single",
  "prompt":"Ancient library at midnight, candlelit. Tall shelves of leather-bound volumes, a long table strewn with open manuscripts, brass astronomical instruments, dark wood, a tabby cat asleep on a book. Hushed, scholarly.",
  "style_profile":"painterly cinematic warm",
  "required_capability":"scene_capable",
  "expected_outputs":["establishing_wide","closer_atmospheric"],
  "evaluation_dimensions":["prompt_adherence","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["prompt_adherence<3","missing cat"] }
```

```json
{ "benchmark_id":"place_023","asset_type":"place_scene","generation_mode":"single",
  "prompt":"Tropical beach at sunrise, calm. Pale sand, gentle pink-gold sky, palm trees with hanging fruit, a single set of footprints leading to the water, no figures present. Serene.",
  "style_profile":"realistic cinematic warm",
  "required_capability":"scene_capable",
  "expected_outputs":["establishing_wide"],
  "evaluation_dimensions":["prompt_adherence","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["prompt_adherence<3","time of day not dawn"] }
```

```json
{ "benchmark_id":"place_024","asset_type":"place_scene","generation_mode":"single",
  "prompt":"Refugee camp under overcast sky. Rows of UN-blue tents, muddy path between, a single supply truck unloading, distant figures carrying water containers, mountains on the horizon. Quiet, somber, not exploitative.",
  "style_profile":"realistic cinematic muted",
  "required_capability":"scene_capable",
  "expected_outputs":["establishing_wide"],
  "evaluation_dimensions":["prompt_adherence","style_adherence","composition_quality","web_app_usability","low_res_readability","mature_policy_compatibility"],
  "failure_conditions":["prompt_adherence<3","depicts identifiable distress in close detail (privacy/dignity)"] }
```

```json
{ "benchmark_id":"place_025","asset_type":"place_scene","generation_mode":"single",
  "prompt":"Corporate lobby with hidden tension. Sleek modern atrium, white marble floor, single piece of abstract sculpture, a receptionist absent from a curved desk, a coffee cup steaming, security camera fish-eye lens prominent in upper corner.",
  "style_profile":"realistic cinematic clean",
  "required_capability":"scene_capable",
  "expected_outputs":["establishing_wide"],
  "evaluation_dimensions":["prompt_adherence","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["prompt_adherence<3","missing security camera"] }
```

## 7. Artifact prompts (25)

Each case targets one or two artifact images. Documents and symbols are
intentionally specific because the variant compatibility matrix
(`docs/architecture/variant-compatibility-matrix.md` §9) treats their
state as canon.

```json
{ "benchmark_id":"artifact_001","asset_type":"artifact","generation_mode":"single",
  "prompt":"Sealed wax-stamped letter on aged cream parchment, dark red wax seal showing a crescent and three stars, edges slightly worn. Photographed on a dark wood table, single warm source from the left.",
  "style_profile":"realistic cinematic warm",
  "required_capability":"scene_capable",
  "expected_outputs":["artifact_card"],
  "evaluation_dimensions":["prompt_adherence","artifact_recognizability","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["artifact_recognizability<4","wax seal absent or different sigil"] }
```

```json
{ "benchmark_id":"artifact_002","asset_type":"artifact","generation_mode":"single",
  "prompt":"Opened handwritten letter, cream paper, neat slanted ink handwriting in English (approximate text fine), signed at the bottom. Slight creases from being folded. Photographed flat under soft daylight.",
  "style_profile":"realistic cinematic clean",
  "required_capability":"scene_capable",
  "expected_outputs":["document_preview"],
  "evaluation_dimensions":["prompt_adherence","artifact_recognizability","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["artifact_recognizability<4","text reads as typed or printed"] }
```

```json
{ "benchmark_id":"artifact_003","asset_type":"artifact","generation_mode":"single",
  "prompt":"Burned and torn letter, partial. Top-left half charred to ash, right half showing fragments of handwriting in faded blue ink. Photographed on a gray surface, soft overhead light.",
  "style_profile":"realistic cinematic muted",
  "required_capability":"scene_capable",
  "expected_outputs":["document_preview"],
  "evaluation_dimensions":["prompt_adherence","artifact_recognizability","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["artifact_recognizability<4","letter appears intact"] }
```

```json
{ "benchmark_id":"artifact_004","asset_type":"artifact","generation_mode":"single",
  "prompt":"Hand-drawn city map on aged vellum, ink lines showing streets and a harbor, compass rose top-right, faint coffee stain bottom-left, edges curled. Top-down view.",
  "style_profile":"painterly cinematic fantasy realism",
  "required_capability":"scene_capable",
  "expected_outputs":["map_preview"],
  "evaluation_dimensions":["prompt_adherence","artifact_recognizability","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["artifact_recognizability<4","compass rose missing"] }
```

```json
{ "benchmark_id":"artifact_005","asset_type":"artifact","generation_mode":"single",
  "prompt":"Modern spiral-bound road atlas, open to a two-page spread, water-damaged with bleeding ink. Photographed flat under cool fluorescent light.",
  "style_profile":"realistic cinematic clean",
  "required_capability":"scene_capable",
  "expected_outputs":["map_preview"],
  "evaluation_dimensions":["prompt_adherence","artifact_recognizability","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["artifact_recognizability<4","atlas appears undamaged"] }
```

```json
{ "benchmark_id":"artifact_006","asset_type":"artifact","generation_mode":"single",
  "prompt":"Ornate brass key, four inches long, intricate cloverleaf bow, complex bit, faint engraving of a crescent on the bow. Photographed on dark velvet, single warm source.",
  "style_profile":"painterly cinematic warm",
  "required_capability":"scene_capable",
  "expected_outputs":["artifact_closeup"],
  "evaluation_dimensions":["prompt_adherence","artifact_recognizability","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["artifact_recognizability<4","crescent engraving absent"] }
```

```json
{ "benchmark_id":"artifact_007","asset_type":"artifact","generation_mode":"single",
  "prompt":"Modern brass apartment key, simple bow, paper tag tied with string, marker writing on tag reading approximately 'APT 4B'. Photographed on a faux-wood counter.",
  "style_profile":"realistic cinematic clean",
  "required_capability":"scene_capable",
  "expected_outputs":["artifact_card"],
  "evaluation_dimensions":["prompt_adherence","artifact_recognizability","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["artifact_recognizability<4","tag absent"] }
```

```json
{ "benchmark_id":"artifact_008","asset_type":"artifact","generation_mode":"single",
  "prompt":"Antique flintlock dueling pistol, walnut grip with silver inlay scrollwork, brass furniture, single-shot. Photographed on a wine-red velvet cloth, candlelit.",
  "style_profile":"painterly cinematic warm",
  "required_capability":"scene_capable",
  "expected_outputs":["artifact_closeup"],
  "evaluation_dimensions":["prompt_adherence","artifact_recognizability","style_adherence","composition_quality","web_app_usability","low_res_readability","mature_policy_compatibility"],
  "failure_conditions":["artifact_recognizability<4","weapon reads as modern firearm"] }
```

```json
{ "benchmark_id":"artifact_009","asset_type":"artifact","generation_mode":"single",
  "prompt":"Modern police-issue 9mm sidearm, matte black, holstered. Photographed in a holster on a desk, top-down view, no rounds visible, no human hand in frame.",
  "style_profile":"realistic cinematic clean",
  "required_capability":"scene_capable",
  "expected_outputs":["artifact_card"],
  "evaluation_dimensions":["prompt_adherence","artifact_recognizability","style_adherence","composition_quality","web_app_usability","low_res_readability","mature_policy_compatibility"],
  "failure_conditions":["artifact_recognizability<4","weapon drawn or pointed"] }
```

```json
{ "benchmark_id":"artifact_010","asset_type":"artifact","generation_mode":"single",
  "prompt":"Faded 1970s Polaroid showing a teenager on a porch in summer, color-shifted slightly toward orange, white border with small scrawled date. Photographed on a wooden surface.",
  "style_profile":"realistic cinematic warm",
  "required_capability":"scene_capable",
  "expected_outputs":["photo_preview"],
  "evaluation_dimensions":["prompt_adherence","artifact_recognizability","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["artifact_recognizability<4","Polaroid border missing"] }
```

```json
{ "benchmark_id":"artifact_011","asset_type":"artifact","generation_mode":"single",
  "prompt":"Modern smartphone photo as displayed on a phone screen — image is of a coffee mug on a table at golden hour. The phone is in frame, hand-held perspective.",
  "style_profile":"realistic cinematic clean",
  "required_capability":"scene_capable",
  "expected_outputs":["photo_preview"],
  "evaluation_dimensions":["prompt_adherence","artifact_recognizability","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["artifact_recognizability<4","phone UI absent"] }
```

```json
{ "benchmark_id":"artifact_012","asset_type":"artifact","generation_mode":"single",
  "prompt":"Ancient bone fetish, finger-length, carved with concentric spirals stained with red ochre, leather thong threaded through one end. Photographed on dark stone.",
  "style_profile":"painterly cinematic muted",
  "required_capability":"scene_capable",
  "expected_outputs":["artifact_closeup"],
  "evaluation_dimensions":["prompt_adherence","artifact_recognizability","style_adherence","composition_quality","web_app_usability","low_res_readability","mature_policy_compatibility"],
  "failure_conditions":["artifact_recognizability<4"] }
```

```json
{ "benchmark_id":"artifact_013","asset_type":"artifact","generation_mode":"single",
  "prompt":"Government warrant, official letterhead at top, blocks of typed text, red 'SEALED' stamp diagonally across, signature at bottom. Photographed flat on a desk under cool overhead light.",
  "style_profile":"realistic cinematic clean",
  "required_capability":"scene_capable",
  "expected_outputs":["notice_or_poster"],
  "evaluation_dimensions":["prompt_adherence","artifact_recognizability","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["artifact_recognizability<4","stamp absent"] }
```

```json
{ "benchmark_id":"artifact_014","asset_type":"artifact","generation_mode":"single",
  "prompt":"Handwritten 'missing' flyer, marker on white printer paper, photograph in the center (small, generic dog), tear-off phone-number tabs at the bottom, taped to a wood telephone pole.",
  "style_profile":"realistic cinematic muted",
  "required_capability":"scene_capable",
  "expected_outputs":["notice_or_poster"],
  "evaluation_dimensions":["prompt_adherence","artifact_recognizability","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["artifact_recognizability<4","tabs absent"] }
```

```json
{ "benchmark_id":"artifact_015","asset_type":"artifact","generation_mode":"single",
  "prompt":"Heraldic crest of House Marenze: a silver crescent over crossed wheat sheaves on a midnight-blue shield, bordered with twisted gold rope, motto banner below in Latin. Centered, flat illustration.",
  "style_profile":"painterly cinematic fantasy realism",
  "required_capability":"scene_capable",
  "expected_outputs":["symbol_or_emblem"],
  "evaluation_dimensions":["prompt_adherence","artifact_recognizability","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["artifact_recognizability<4","crescent missing or recolored","sheaves absent"] }
```

```json
{ "benchmark_id":"artifact_016","asset_type":"artifact","generation_mode":"single",
  "prompt":"Corporate logo for fictional 'Velarion Energy': stylized angular V intertwined with a stylized power-bolt, deep green on white background, sans-serif wordmark beneath. Centered, flat vector style.",
  "style_profile":"corporate clean modern",
  "required_capability":"scene_capable",
  "expected_outputs":["symbol_or_emblem"],
  "evaluation_dimensions":["prompt_adherence","artifact_recognizability","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["artifact_recognizability<4","V and bolt not intertwined"] }
```

```json
{ "benchmark_id":"artifact_017","asset_type":"artifact","generation_mode":"single",
  "prompt":"Cult sigil drawn in white chalk on dark stone: a three-pointed star inside a broken circle, three small dots in the spaces between points. No human hands in frame.",
  "style_profile":"horror cinematic muted",
  "required_capability":"scene_capable",
  "expected_outputs":["symbol_or_emblem"],
  "evaluation_dimensions":["prompt_adherence","artifact_recognizability","style_adherence","composition_quality","web_app_usability","low_res_readability","mature_policy_compatibility"],
  "failure_conditions":["artifact_recognizability<4","circle not broken"] }
```

```json
{ "benchmark_id":"artifact_018","asset_type":"artifact","generation_mode":"single",
  "prompt":"Cotton handkerchief with a dark dried bloodstain in the center, folded asymmetrically, in an evidence bag on a steel surface, evidence tag attached.",
  "style_profile":"realistic cinematic clean",
  "required_capability":"scene_capable",
  "expected_outputs":["artifact_closeup"],
  "evaluation_dimensions":["prompt_adherence","artifact_recognizability","style_adherence","composition_quality","web_app_usability","low_res_readability","mature_policy_compatibility"],
  "failure_conditions":["artifact_recognizability<4","evidence tag absent"] }
```

```json
{ "benchmark_id":"artifact_019","asset_type":"artifact","generation_mode":"single",
  "prompt":"Broken pocket watch, brass case, cracked crystal, hour hand bent, engraving on back reading approximately 'For E.V.', open face visible. Photographed on dark felt.",
  "style_profile":"realistic cinematic warm",
  "required_capability":"scene_capable",
  "expected_outputs":["artifact_closeup"],
  "evaluation_dimensions":["prompt_adherence","artifact_recognizability","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["artifact_recognizability<4","watch appears undamaged"] }
```

```json
{ "benchmark_id":"artifact_020","asset_type":"artifact","generation_mode":"single",
  "prompt":"Encrypted USB drive, matte black with single red LED, attached to a small steel keyring, lying on a manila folder with a redaction stamp.",
  "style_profile":"realistic cinematic noir",
  "required_capability":"scene_capable",
  "expected_outputs":["artifact_closeup"],
  "evaluation_dimensions":["prompt_adherence","artifact_recognizability","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["artifact_recognizability<4","LED not visible"] }
```

```json
{ "benchmark_id":"artifact_021","asset_type":"artifact","generation_mode":"single",
  "prompt":"Singed leather journal open to a page of looping ink handwriting (approximate text fine), the right page edge burned away in a curve, ash on the surrounding desk. Single warm source.",
  "style_profile":"painterly cinematic warm",
  "required_capability":"scene_capable",
  "expected_outputs":["document_preview"],
  "evaluation_dimensions":["prompt_adherence","artifact_recognizability","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["artifact_recognizability<4","page edge appears intact"] }
```

```json
{ "benchmark_id":"artifact_022","asset_type":"artifact","generation_mode":"single",
  "prompt":"Forged identity card, plastic, photograph slightly mis-aligned with the printed border, hologram strip missing, faintly different font weight in one field. Photographed flat under cool light.",
  "style_profile":"realistic cinematic clean",
  "required_capability":"scene_capable",
  "expected_outputs":["artifact_card"],
  "evaluation_dimensions":["prompt_adherence","artifact_recognizability","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["artifact_recognizability<4","card looks perfectly authentic"] }
```

```json
{ "benchmark_id":"artifact_023","asset_type":"artifact","generation_mode":"single",
  "prompt":"Typed dossier on cream paper, two-column layout, two black redaction bars across the second paragraph, classified header at the top, stapled top-left corner. Photographed flat under cool light.",
  "style_profile":"realistic cinematic noir",
  "required_capability":"scene_capable",
  "expected_outputs":["document_preview"],
  "evaluation_dimensions":["prompt_adherence","artifact_recognizability","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["artifact_recognizability<4","redaction bars absent"] }
```

```json
{ "benchmark_id":"artifact_024","asset_type":"artifact","generation_mode":"single",
  "prompt":"Medieval scroll partially unrolled on a wooden table, dark ink calligraphy across yellowed paper, red wax seal hanging from a ribbon at the bottom edge. Candlelit warm light.",
  "style_profile":"painterly cinematic warm",
  "required_capability":"scene_capable",
  "expected_outputs":["document_preview"],
  "evaluation_dimensions":["prompt_adherence","artifact_recognizability","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["artifact_recognizability<4","wax seal absent"] }
```

```json
{ "benchmark_id":"artifact_025","asset_type":"artifact","generation_mode":"single",
  "prompt":"Modern signed contract, multi-page on white printer paper, top page visible with signature lines, blue-ink signature on each line, embossed notary stamp in the lower right.",
  "style_profile":"realistic cinematic clean",
  "required_capability":"scene_capable",
  "expected_outputs":["document_preview"],
  "evaluation_dimensions":["prompt_adherence","artifact_recognizability","style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["artifact_recognizability<4","notary stamp absent"] }
```

## 8. Consistency stress tests (25)

Each case has an `anchor_prompt` and a list of `variants` to be generated
referencing the anchor (via reference-image conditioning, seed pinning,
or LoRA — whichever the candidate supports per PRD 03 §8). Reviewers
score `identity_consistency` (characters) or `place_consistency`
(places) for each variant **against the anchor**.

```json
{ "benchmark_id":"hard_001","asset_type":"character_consistency","generation_mode":"pack",
  "anchor_prompt":"see char_001",
  "variants":[
    {"variant_key":"neutral_front","modifier":""},
    {"variant_key":"warm","modifier":"faint warm smile, eyes softer"},
    {"variant_key":"serious","modifier":"eyes set, jaw tense, lips closed"},
    {"variant_key":"tense","modifier":"brow drawn, shoulders forward"},
    {"variant_key":"angry","modifier":"controlled anger, teeth slightly bared"}
  ],
  "style_profile":"realistic cinematic noir",
  "required_capability":"pack_capable",
  "evaluation_dimensions":["identity_consistency","prompt_adherence","style_adherence","composition_quality","low_res_readability"],
  "failure_conditions":["identity_consistency<4 on any variant"] }
```

```json
{ "benchmark_id":"hard_002","asset_type":"character_consistency","generation_mode":"pack",
  "anchor_prompt":"see char_001",
  "variants":[
    {"variant_key":"angle_front","modifier":"front view, eyes to camera"},
    {"variant_key":"angle_three_quarter","modifier":"three-quarter, looking slightly off-camera"},
    {"variant_key":"angle_side","modifier":"profile from left side"}
  ],
  "style_profile":"realistic cinematic noir",
  "required_capability":"pack_capable",
  "evaluation_dimensions":["identity_consistency","prompt_adherence","style_adherence","composition_quality","low_res_readability"],
  "failure_conditions":["identity_consistency<4 on any variant","scar position inconsistent between angles"] }
```

```json
{ "benchmark_id":"hard_003","asset_type":"character_consistency","generation_mode":"pack",
  "anchor_prompt":"see char_001",
  "variants":[
    {"variant_key":"lighting_daylight","modifier":"natural daylight, window front"},
    {"variant_key":"lighting_candle","modifier":"single warm candle source from below"},
    {"variant_key":"lighting_fluorescent","modifier":"harsh overhead fluorescent, slight color cast"}
  ],
  "style_profile":"realistic cinematic noir",
  "required_capability":"pack_capable",
  "evaluation_dimensions":["identity_consistency","style_adherence","composition_quality","low_res_readability"],
  "failure_conditions":["identity_consistency<4 on any variant"] }
```

```json
{ "benchmark_id":"hard_004","asset_type":"character_consistency","generation_mode":"pack",
  "anchor_prompt":"see char_006 (Eitha the Ranger)",
  "variants":[
    {"variant_key":"neutral_front","modifier":""},
    {"variant_key":"warm","modifier":"slight smile, eyes amused"},
    {"variant_key":"serious","modifier":"jaw set, eyes scanning"},
    {"variant_key":"tense","modifier":"brow lowered, hand near belt"},
    {"variant_key":"resolute","modifier":"clear-eyed, chin level"}
  ],
  "style_profile":"painterly cinematic fantasy realism",
  "required_capability":"pack_capable",
  "evaluation_dimensions":["identity_consistency","prompt_adherence","style_adherence","composition_quality","low_res_readability"],
  "failure_conditions":["identity_consistency<4 on any variant","pine-needle tattoo inconsistent"] }
```

```json
{ "benchmark_id":"hard_005","asset_type":"character_consistency","generation_mode":"pack",
  "anchor_prompt":"see char_006 (Eitha the Ranger)",
  "variants":[
    {"variant_key":"angle_front","modifier":""},
    {"variant_key":"angle_three_quarter","modifier":""},
    {"variant_key":"angle_side","modifier":"profile from right side"}
  ],
  "style_profile":"painterly cinematic fantasy realism",
  "required_capability":"pack_capable",
  "evaluation_dimensions":["identity_consistency","style_adherence","composition_quality","low_res_readability"],
  "failure_conditions":["identity_consistency<4 on any variant"] }
```

```json
{ "benchmark_id":"hard_006","asset_type":"character_consistency","generation_mode":"pack",
  "anchor_prompt":"see char_011 (Rax-7)",
  "variants":[
    {"variant_key":"neutral_front","modifier":""},
    {"variant_key":"warm","modifier":"rare faint smile (uncomfortable)"},
    {"variant_key":"serious","modifier":"intimidating stillness"},
    {"variant_key":"tense","modifier":"hand drifting toward holster"},
    {"variant_key":"injured","modifier":"chrome arm dented and sparking; otherwise unchanged"}
  ],
  "style_profile":"sci-fi cinematic gritty",
  "required_capability":"pack_capable",
  "evaluation_dimensions":["identity_consistency","prompt_adherence","style_adherence","composition_quality","low_res_readability"],
  "failure_conditions":["identity_consistency<4 on any variant","chromed arm or implant color shifts"] }
```

```json
{ "benchmark_id":"hard_007","asset_type":"place_consistency","generation_mode":"pack",
  "anchor_prompt":"see place_006 (street market, midday)",
  "variants":[
    {"variant_key":"day_view","modifier":"midday, vibrant, as anchor"},
    {"variant_key":"night_view","modifier":"same market at 9pm: half stalls closed, string lights, fewer people, different palette but same architecture/landmarks"}
  ],
  "style_profile":"realistic cinematic warm",
  "required_capability":"scene_capable",
  "evaluation_dimensions":["place_consistency","prompt_adherence","style_adherence","composition_quality","low_res_readability"],
  "failure_conditions":["place_consistency<4 on any variant","cobblestone pattern visibly changes between day/night"] }
```

```json
{ "benchmark_id":"hard_008","asset_type":"place_consistency","generation_mode":"pack",
  "anchor_prompt":"see place_006 (street market)",
  "variants":[
    {"variant_key":"calm_empty","modifier":"dawn, no people, stalls being set up, same architecture"},
    {"variant_key":"busy_active","modifier":"as anchor, midday peak crowd"}
  ],
  "style_profile":"realistic cinematic warm",
  "required_capability":"scene_capable",
  "evaluation_dimensions":["place_consistency","prompt_adherence","style_adherence","composition_quality","low_res_readability"],
  "failure_conditions":["place_consistency<4 on any variant"] }
```

```json
{ "benchmark_id":"hard_009","asset_type":"place_consistency","generation_mode":"pack",
  "anchor_prompt":"see place_010 (stone temple at dawn)",
  "variants":[
    {"variant_key":"state_intact","modifier":"as anchor"},
    {"variant_key":"state_damaged","modifier":"same temple after fire and partial collapse: roof beam down across the altar, scorch marks up the columns, daylight visible through holes — same architecture, same green marble altar"}
  ],
  "style_profile":"painterly cinematic fantasy realism",
  "required_capability":"scene_capable",
  "evaluation_dimensions":["place_consistency","prompt_adherence","style_adherence","composition_quality","low_res_readability","mature_policy_compatibility"],
  "failure_conditions":["place_consistency<4 on any variant","altar color or position changes"] }
```

```json
{ "benchmark_id":"hard_010","asset_type":"place_consistency","generation_mode":"pack",
  "anchor_prompt":"see place_013 (sci-fi command bridge)",
  "variants":[
    {"variant_key":"normal_ops","modifier":"as anchor, calm blue lighting"},
    {"variant_key":"emergency","modifier":"same bridge under red alert: lighting shifts to red, crew tense, console alarms — same console layout, same viewport, same planet visible"}
  ],
  "style_profile":"sci-fi cinematic clean",
  "required_capability":"scene_capable",
  "evaluation_dimensions":["place_consistency","prompt_adherence","style_adherence","composition_quality","low_res_readability"],
  "failure_conditions":["place_consistency<4 on any variant","console layout reorganizes"] }
```

```json
{ "benchmark_id":"hard_011","asset_type":"place_consistency","generation_mode":"pack",
  "anchor_prompt":"see place_006 (street market)",
  "variants":[
    {"variant_key":"weather_clear","modifier":"as anchor"},
    {"variant_key":"weather_rain","modifier":"same market in evening rain: shoppers under awnings, puddles, wet cobblestones reflecting awning colors — same stalls and landmarks"}
  ],
  "style_profile":"realistic cinematic warm",
  "required_capability":"scene_capable",
  "evaluation_dimensions":["place_consistency","prompt_adherence","style_adherence","composition_quality","low_res_readability"],
  "failure_conditions":["place_consistency<4 on any variant"] }
```

```json
{ "benchmark_id":"hard_012","asset_type":"artifact_consistency","generation_mode":"pack",
  "anchor_prompt":"see artifact_001 (sealed wax-stamped letter)",
  "variants":[
    {"variant_key":"state_sealed","modifier":"as anchor"},
    {"variant_key":"state_opened","modifier":"same parchment now opened flat, wax seal broken into two pieces beside it, same crescent-and-stars design intact in the wax"}
  ],
  "style_profile":"realistic cinematic warm",
  "required_capability":"scene_capable",
  "evaluation_dimensions":["artifact_recognizability","prompt_adherence","style_adherence","composition_quality","low_res_readability"],
  "failure_conditions":["artifact_recognizability<4 on any variant","wax sigil changes between states"] }
```

```json
{ "benchmark_id":"hard_013","asset_type":"artifact_consistency","generation_mode":"pack",
  "anchor_prompt":"see artifact_018 (handkerchief)",
  "variants":[
    {"variant_key":"state_clean","modifier":"same handkerchief, freshly laundered, white, neatly folded, in a wooden tray on a dressing table"},
    {"variant_key":"state_bloodstained","modifier":"as anchor"}
  ],
  "style_profile":"realistic cinematic clean",
  "required_capability":"scene_capable",
  "evaluation_dimensions":["artifact_recognizability","prompt_adherence","style_adherence","composition_quality","low_res_readability","mature_policy_compatibility"],
  "failure_conditions":["artifact_recognizability<4 on any variant"] }
```

```json
{ "benchmark_id":"hard_014","asset_type":"artifact_consistency","generation_mode":"pack",
  "anchor_prompt":"see artifact_009 (modern sidearm)",
  "variants":[
    {"variant_key":"state_intact","modifier":"as anchor"},
    {"variant_key":"state_jammed","modifier":"same weapon, slide locked back, ejected casing pinched in port, same matte black finish"}
  ],
  "style_profile":"realistic cinematic clean",
  "required_capability":"scene_capable",
  "evaluation_dimensions":["artifact_recognizability","prompt_adherence","style_adherence","composition_quality","low_res_readability","mature_policy_compatibility"],
  "failure_conditions":["artifact_recognizability<4 on any variant","weapon depicted in use"] }
```

```json
{ "benchmark_id":"hard_015","asset_type":"artifact_consistency","generation_mode":"pack",
  "anchor_prompt":"see artifact_006 (ornate brass key)",
  "variants":[
    {"variant_key":"state_clean","modifier":"as anchor"},
    {"variant_key":"state_aged","modifier":"same key, decades later: deep brass patina, rust spot on bow, scratched bit, same cloverleaf shape and crescent engraving"}
  ],
  "style_profile":"painterly cinematic warm",
  "required_capability":"scene_capable",
  "evaluation_dimensions":["artifact_recognizability","prompt_adherence","style_adherence","composition_quality","low_res_readability"],
  "failure_conditions":["artifact_recognizability<4 on any variant","key shape changes"] }
```

```json
{ "benchmark_id":"hard_016","asset_type":"cross_asset_style_consistency","generation_mode":"variant",
  "anchor_prompt":"style profile: dark cinematic noir",
  "variants":[
    {"variant_key":"character","modifier":"see char_001"},
    {"variant_key":"place","modifier":"see place_001"},
    {"variant_key":"artifact","modifier":"see artifact_020 (encrypted USB)"}
  ],
  "style_profile":"realistic cinematic noir",
  "required_capability":"scene_capable",
  "evaluation_dimensions":["style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["style_adherence<4 on any output — palettes / lighting should read as one world"] }
```

```json
{ "benchmark_id":"hard_017","asset_type":"cross_asset_style_consistency","generation_mode":"variant",
  "anchor_prompt":"style profile: painterly cinematic fantasy realism",
  "variants":[
    {"variant_key":"character","modifier":"see char_006 (Eitha)"},
    {"variant_key":"place","modifier":"see place_011 (fantasy village square)"},
    {"variant_key":"artifact","modifier":"see artifact_004 (hand-drawn city map)"}
  ],
  "style_profile":"painterly cinematic fantasy realism",
  "required_capability":"scene_capable",
  "evaluation_dimensions":["style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["style_adherence<4 on any output"] }
```

```json
{ "benchmark_id":"hard_018","asset_type":"cross_asset_style_consistency","generation_mode":"variant",
  "anchor_prompt":"style profile: high-key clean modern",
  "variants":[
    {"variant_key":"character","modifier":"see char_022 (Aisha Bello)"},
    {"variant_key":"place","modifier":"see place_025 (corporate lobby)"},
    {"variant_key":"artifact","modifier":"see artifact_011 (smartphone photo)"}
  ],
  "style_profile":"corporate clean modern",
  "required_capability":"scene_capable",
  "evaluation_dimensions":["style_adherence","composition_quality","web_app_usability","low_res_readability"],
  "failure_conditions":["style_adherence<4 on any output"] }
```

```json
{ "benchmark_id":"hard_019","asset_type":"multi_entity_scene","generation_mode":"single",
  "anchor_prompt":"Detective Ana Vass (char_001) seated at the steel table in the interrogation room (place_001), the encrypted USB drive (artifact_020) on the table between her hands. Composition leaves space on the left for a participant pane overlay.",
  "variants":[
    {"variant_key":"composed_scene","modifier":""}
  ],
  "style_profile":"realistic cinematic noir",
  "required_capability":"pack_capable",
  "evaluation_dimensions":["prompt_adherence","identity_consistency","place_consistency","artifact_recognizability","style_adherence","composition_quality","web_app_usability"],
  "failure_conditions":["identity_consistency<4","place_consistency<4","artifact_recognizability<4","subject obscures left-edge composition reservation"] }
```

```json
{ "benchmark_id":"hard_020","asset_type":"character_consistency","generation_mode":"pack",
  "anchor_prompt":"see char_001 (Detective Ana Vass)",
  "variants":[
    {"variant_key":"outfit_formal","modifier":"black tailored suit, single strand of pearls"},
    {"variant_key":"outfit_work","modifier":"as anchor"},
    {"variant_key":"outfit_combat","modifier":"tactical vest over the gray sweater, sidearm holstered"},
    {"variant_key":"outfit_casual","modifier":"oversized college sweatshirt, jeans"},
    {"variant_key":"outfit_intimate","modifier":"plain cotton tank, soft cardigan — implied off-duty, no nudity"},
    {"variant_key":"outfit_travel","modifier":"hooded raincoat, duffel bag strap visible"}
  ],
  "style_profile":"realistic cinematic noir",
  "required_capability":"pack_capable",
  "evaluation_dimensions":["identity_consistency","prompt_adherence","style_adherence","composition_quality","low_res_readability","mature_policy_compatibility"],
  "failure_conditions":["identity_consistency<4 on any variant","scar inconsistent across outfits"] }
```

```json
{ "benchmark_id":"hard_021","asset_type":"character_consistency","generation_mode":"pack",
  "anchor_prompt":"see char_023 (Nonna Catarina, 82)",
  "variants":[
    {"variant_key":"age_young_adult","modifier":"same person at 28: same eye color, same gentle hands, dark hair, fewer lines"},
    {"variant_key":"age_middle","modifier":"same person at 55: hair beginning to gray, soft lines around eyes"},
    {"variant_key":"age_current","modifier":"as anchor (82)"},
    {"variant_key":"age_very_old","modifier":"same person at 92: thinner frame, hair full white, same warm smile"}
  ],
  "style_profile":"realistic cinematic warm",
  "required_capability":"pack_capable",
  "evaluation_dimensions":["identity_consistency","prompt_adherence","style_adherence","composition_quality","low_res_readability"],
  "failure_conditions":["identity_consistency<4 on any variant","family resemblance lost across ages"] }
```

```json
{ "benchmark_id":"hard_022","asset_type":"place_consistency","generation_mode":"pack",
  "anchor_prompt":"see place_006 (street market)",
  "variants":[
    {"variant_key":"season_spring","modifier":"cherry petals on cobblestones, bright fresh produce"},
    {"variant_key":"season_summer","modifier":"as anchor"},
    {"variant_key":"season_autumn","modifier":"piles of squash and persimmons, vendors in light jackets, gold leaves drifting"},
    {"variant_key":"season_winter","modifier":"snow on awnings, fewer stalls, breath visible in cold air, same architecture and cobblestones"}
  ],
  "style_profile":"realistic cinematic warm",
  "required_capability":"scene_capable",
  "evaluation_dimensions":["place_consistency","prompt_adherence","style_adherence","composition_quality","low_res_readability"],
  "failure_conditions":["place_consistency<4 on any variant","architecture changes between seasons"] }
```

```json
{ "benchmark_id":"hard_023","asset_type":"place_consistency","generation_mode":"pack",
  "anchor_prompt":"see place_006 (street market)",
  "variants":[
    {"variant_key":"weather_clear","modifier":"as anchor"},
    {"variant_key":"weather_rain","modifier":"steady rain, shoppers under awnings, puddles reflecting awning colors"},
    {"variant_key":"weather_storm","modifier":"heavy wind, half the awnings blown taut, stalls scrambling to close, dim leaden sky"},
    {"variant_key":"weather_fog","modifier":"heavy morning fog, vendors as silhouettes, distant stalls dissolving"}
  ],
  "style_profile":"realistic cinematic warm",
  "required_capability":"scene_capable",
  "evaluation_dimensions":["place_consistency","prompt_adherence","style_adherence","composition_quality","low_res_readability"],
  "failure_conditions":["place_consistency<4 on any variant","stall layout reshuffles"] }
```

```json
{ "benchmark_id":"hard_024","asset_type":"artifact_consistency","generation_mode":"pack",
  "anchor_prompt":"see artifact_001 (sealed wax-stamped letter)",
  "variants":[
    {"variant_key":"degradation_new","modifier":"fresh, parchment bright, seal pristine"},
    {"variant_key":"degradation_used","modifier":"folded and unfolded several times, slight creasing, seal cracked"},
    {"variant_key":"degradation_old","modifier":"yellowed parchment, edges frayed, wax brittle and chipped"},
    {"variant_key":"degradation_destroyed","modifier":"burned to fragments — only the wax sigil and a charred corner survive"}
  ],
  "style_profile":"realistic cinematic warm",
  "required_capability":"scene_capable",
  "evaluation_dimensions":["artifact_recognizability","prompt_adherence","style_adherence","composition_quality","low_res_readability"],
  "failure_conditions":["artifact_recognizability<4 on any variant","wax sigil changes design across stages"] }
```

```json
{ "benchmark_id":"hard_025","asset_type":"multi_entity_scene","generation_mode":"single",
  "anchor_prompt":"Eitha the Ranger (char_006) standing in the fantasy village square (place_011) at dusk, holding the hand-drawn city map (artifact_004) half-unrolled. Composition leaves bottom third clear for a dialog overlay.",
  "variants":[
    {"variant_key":"composed_scene","modifier":""}
  ],
  "style_profile":"painterly cinematic fantasy realism",
  "required_capability":"pack_capable",
  "evaluation_dimensions":["prompt_adherence","identity_consistency","place_consistency","artifact_recognizability","style_adherence","composition_quality","web_app_usability"],
  "failure_conditions":["identity_consistency<4","place_consistency<4","artifact_recognizability<4","subject obscures bottom-third composition reservation"] }
```

## 9. Result row schema (for the runner)

Every benchmark run produces one result row per (benchmark_id × variant
× provider × model). Shape:

```json
{
  "benchmark_run_id": "run_2026_06_05_001",
  "benchmark_id": "char_001",
  "variant_key": "warm",
  "provider_id": "bfl",
  "model_id": "flux-2-klein",
  "asset_id": "asset_abc",
  "preview_latency_ms": 4100,
  "final_latency_ms": 22000,
  "estimated_cost_usd": "0.04",
  "actual_cost_usd": "0.038",
  "seed": "12345",
  "reference_asset_ids": ["asset_anchor_xyz"],
  "operational_pass": true,
  "scores": {
    "prompt_adherence": 4,
    "identity_consistency": 5,
    "style_adherence": 4,
    "composition_quality": 4,
    "web_app_usability": 5,
    "low_res_readability": 4,
    "generation_artifacts": 5
  },
  "rubric_pass": true,
  "reviewer_id": "reviewer_a",
  "notes": "Scar slightly faint at warm expression."
}
```

A run's pass/fail aggregate (per §4):

- Operational pass = all checks in §3.2 satisfied.
- Rubric pass = no hard-fail floors breached (identity ≥ 4 for
  consistency cases, place ≥ 4 for place-consistency cases, low-res ≥ 3
  for web-app preview).
- Run pass = operational pass AND rubric pass.

## 10. Extension notes

- **LLM / image-judge scoring**: when added, each LLM-scored result row
  carries a sibling `scores_experimental` block and an
  `experimental: true` flag. The aggregate must not promote a candidate
  based on experimental scores alone.
- **Adding cases**: increment the per-category counter (`char_026`,
  `place_026`, etc.). Keep JSON shape stable; the runner depends on it.
- **Style profile vocabulary**: the `style_profile` strings here are
  human prompts. When the platform's `StyleProfile` resource lands,
  swap to `style_profile_id` references.
- **Mature content cases**: marked with `mature_policy_compatibility`
  in evaluation dimensions. These exist to detect provider
  over-refusal as much as under-refusal; both are failure modes for
  DreamChat (PRD 01 §7 creative-policy stance).
- **Composition reservations** (`hard_019`, `hard_025`): some UI
  surfaces overlay text or controls in specific regions. Those cases
  test that the generator can leave space deliberately.

---

## Confidence to Implement

**Score: 88/100 — High** *(was 60; +28 after corpus + rubric landed)*

The corpus contains 100 real prompts across the four required categories,
with explicit `required_capability` per case so it ties cleanly into PRD 03
§8's provider capability levels. The rubric (§3) is precise enough that two
human reviewers can score independently and compare. The operational
checks (§3.2) are machine-checkable. The result-row schema (§9) gives the
runner an unambiguous output shape. Hard-fail floors (§4) connect scores
back to capability classification.

Why not higher:

- The runner itself still has to be written (orchestrate the API calls,
  collect results, surface them to a reviewer UI). That's straightforward
  but not done.
- LLM-judge / image-similarity scoring is mentioned as a future addition
  but its rubric mapping (CLIP score → 1–5 mapping, vision-LLM prompt
  template) is not specified.
- Some `style_profile` strings are human prompts; once the
  `StyleProfile` resource lands they should be swapped for
  `style_profile_id` references — pure mechanical update, not blocking.
