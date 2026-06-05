# Asset Versioning and Consistency

## Core Rule

A character or place should not be treated as a prompt.

It should have a persistent visual identity record with versions and variants.

## Version vs Variant

### Variant

A variant is a different view of the same visual state.

Examples:

- neutral expression
- angry expression
- 3/4 angle
- night lighting
- rainy version
- empty street
- busy street

### Version

A version is a meaningful visual state change.

Examples:

- character gains a scar
- character changes age significantly
- character receives a permanent transformation
- place burns down
- place is rebuilt
- artifact breaks or changes form

## Character Consistency

Each recurring character should have:

- canonical visual traits
- consistency key
- anchor images
- stable style profile
- variant assets
- version history

Future generations should use:

- canonical traits
- anchor assets when provider supports references
- seed/consistency strategy
- explicit version number

## Place Consistency

Each recurring place should have:

- architectural traits
- key landmarks
- visual mood
- lighting defaults
- known state
- anchor assets
- version history

## Starter Packs

When an important character is created, generate a starter pack.

Recommended initial character pack:

```txt
neutral_front
neutral_three_quarter
side_profile
warm_expression
tense_expression
angry_expression
surprised_expression
```

Recommended initial place pack:

```txt
establishing_wide
closer_atmospheric
day_view
night_view
empty_view
active_view
```

## Retrieval Before Generation

Before generating a new asset, the platform should search existing assets:

```txt
1. exact owner + variant + version + style match
2. compatible variant for same visual identity
3. previous anchor asset
4. generation required
```

## Invalidation Rules

Create a new version when:

- canonical visual traits change permanently
- place state changes substantially
- artifact state changes substantially
- user explicitly asks to update canonical appearance

Create a variant when:

- expression changes
- angle changes
- time of day changes
- weather changes
- framing changes

## Prompt Hashing

Every generated asset should store a prompt hash.

Hash input should include:

- normalized prompt
- normalized negative prompt
- style profile ID/version
- visual identity version
- asset variant key
- provider model ID

This supports deduplication and reproducibility.
