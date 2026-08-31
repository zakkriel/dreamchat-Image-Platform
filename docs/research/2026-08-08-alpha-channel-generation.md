# Alpha-channel generation for character emotion packs

**Date:** 2026-08-08
**Question:** what is the best way to produce transparent-background (true alpha) character sprites, given we must keep a recurring character identical across images?
**Status:** research note. No Go source was modified.

---

## 0. What we do today, and why this note exists

`internal/jobs/background.go` makes a second provider call per cell: the Kontext render is POSTed to `https://fal.run/fal-ai/birefnet` with `model: "Portrait"`, `output_format: "png"`, `sync_mode: true`, as a base64 data URI, and the returned PNG replaces the render. Every parameter we send is confirmed by fal's own schema for that endpoint — `model` enum includes `"Portrait"` (mapped to `BiRefNet-portrait-TR_P3M_10k-epoch_120.pth`), `output_format` enum is `webp|png|gif`, and `sync_mode` "returns the media as a data URI" ([fal-ai/birefnet model card](https://fal.ai/models/fal-ai/birefnet)).

Two calls per cell on our most expensive path. This note establishes whether one call can do the job.

**Notation.** Claims marked **UNVERIFIED** could not be established from a primary source. They are labelled, not guessed.

---

## 1. Native alpha generation — single-call candidates

"True RGBA natively" means the model emits an alpha channel as part of generation, with no separate matting pass.

### 1.1 OpenAI GPT Image (`gpt-image-1`, `gpt-image-1.5`, `gpt-image-2`)

**The parameter exists and it is on the reference-image endpoint.** `POST /v1/images/edits` accepts:

- `images`: "Input image references to edit. For GPT image models, you can provide **up to 16 images**." Each entry is `{file_id}` or `{image_url}` (a URL *or* a base64 data URL) ([Images API reference](https://developers.openai.com/api/reference/resources/images)).
- `background`: `"transparent" | "opaque" | "auto" | null` — *"Set the background of the generated image output. Transparent backgrounds are available for supported GPT Image models. For `gpt-image-2` and `gpt-image-2-2026-04-21`, this support is in preview. **When using `transparent`, set the output format to `png` or `webp`.**"* ([Images API reference](https://developers.openai.com/api/reference/resources/images))
- `output_format`: `png | jpeg | webp`. The guide is explicit: *"Transparent backgrounds are available in preview for `gpt-image-2`. Set `background: \"transparent\"` to request one. Use `png` (the default) or `webp`; `jpeg` isn't supported with transparent backgrounds."* ([Image generation guide](https://developers.openai.com/api/docs/guides/image-generation))

**Semantics.** `background` is a *generation instruction*, not a post-process: the response object echoes `background: "transparent" | "opaque"` alongside `output_format` and `usage`, i.e. it is a property of the render, and `auto` lets the model choose ("`size`, `quality`, and `background` support the `auto` option, where the model will automatically select the best option based on the prompt" — [guide](https://developers.openai.com/api/docs/guides/image-generation)). Straight vs premultiplied is **UNVERIFIED** from OpenAI docs — but PNG cannot carry premultiplied alpha (see §4.1), so any PNG they return is straight alpha by format definition.

**Reference-image identity.** Supported, and on `gpt-image-2` it is not optional: *"The `input_fidelity` parameter controls how strongly a model preserves details from input images during edits and reference-image workflows. For `gpt-image-2`, omit this parameter; the API doesn't allow changing it because the model processes every image input at high fidelity automatically."* ([guide](https://developers.openai.com/api/docs/guides/image-generation)). For `gpt-image-1` / `gpt-image-1.5` the parameter is exposed as `input_fidelity: "high" | "low"` ([API reference](https://developers.openai.com/api/reference/resources/images)).

**Price (vendor's own pricing page and cost table).** Per-image *output* cost at 1024×1024 ([pricing](https://developers.openai.com/api/docs/pricing#image-generation), [cost table](https://developers.openai.com/api/docs/guides/image-generation)):

| Model | Low | Medium | High |
|---|---|---|---|
| GPT Image 2 | \$0.006 | \$0.053 | \$0.211 |
| GPT Image 1.5 | \$0.009 | \$0.034 | \$0.133 |
| GPT Image 1 | \$0.011 | \$0.042 | \$0.167 |
| GPT Image 1 Mini | \$0.005 | \$0.011 | \$0.036 |

That is **output only**. OpenAI is explicit that *"The final cost is the sum of: input text tokens, input image tokens if using the edits endpoint, image output tokens"*, and warns that *"Because `gpt-image-2` always processes image inputs at high fidelity, edit requests that include reference images can use more input tokens"* ([guide](https://developers.openai.com/api/docs/guides/image-generation)). Image input for `gpt-image-2` is **\$8.00 / 1M tokens** (\$2.00 cached) ([pricing](https://developers.openai.com/api/docs/pricing)). The exact input-token count for a 1024×1024 anchor image at forced-high fidelity is **UNVERIFIED** — OpenAI publishes a calculator, not a table, for `gpt-image-2`. Anchor-image caching at \$2.00/1M is the lever that would make this cheap across a pack, since the same anchor is re-sent for every cell.

**Availability on fal.ai:** not applicable — this is OpenAI's own API, not a fal endpoint.

**Censorship — this is the blocker.** The moderation control is `moderation: "low" | "auto" | null` ([API reference](https://developers.openai.com/api/reference/resources/images)). There is no *off*. Requests fail with error code `moderation_blocked` carrying `moderation_details.categories` and `moderation_details.moderation_stage`, where stage is `"input"` **or** `"output"` — i.e. a render that completes can still be withheld ([guide, error-handling section](https://developers.openai.com/api/docs/guides/image-generation)). Use of GPT Image models also requires [API Organization Verification](https://help.openai.com/en/articles/10910291-api-organization-verification) ([guide](https://developers.openai.com/api/docs/guides/image-generation)). Against a stated program goal of NO CENSORSHIP, a two-stage moderation gate with no opt-out is a material regression from fal's `safety_tolerance` dial (§1.5).

### 1.2 Recraft V3 — the "native transparent PNG" claim does not survive contact with the API

Recraft's own API reference lists every parameter of `POST /v1/images/generations` — `prompt`, `n`, `model`, `size`, `style`, `style_id`, `style_match`, `style_references`, `style_reference_urls`, `negative_prompt`, `random_seed`, `response_format`, `text_layout`, `controls` — and **none of them requests a transparent background** ([Recraft endpoints](https://www.recraft.ai/docs/api-reference/endpoints.md)). The only `background_color` in the API is inside `controls` / `palette`, and it is a *preferable colour* hint (`{'rgb': [40, 40, 40]}`), i.e. an opaque colour, not alpha ([same page, Colors section](https://www.recraft.ai/docs/api-reference/endpoints.md)). fal's mirror of the model exposes only `prompt`, `image_size`, `style`, `colors`, `style_id`, `enable_safety_checker` ([fal-ai/recraft/v3/text-to-image](https://fal.ai/models/fal-ai/recraft/v3/text-to-image)), and its example output is a `.webp`.

Recraft *does* have transparency — as a **separate `/v1/images/removeBackground` call** billed at **\$0.01 per request**, distinct from the \$0.04-per-image V3 raster generation ([Recraft API pricing](https://www.recraft.ai/docs/api-reference/pricing.md)). So Recraft is a **two-call** architecture too, not a native-alpha one.

**Verdict: the "Recraft V3 emits native transparent PNG" claim is not supported by Recraft's own API reference.** It is a background-removal product, priced as one.

Recraft *is* interesting for identity: `style_references` / `style_reference_urls` accept up to 10 images (≤64 MB total, ≤10 MB each) and the server mints a private `style_id` you can reuse — billed compositely, \$0.005 per style creation plus per-image generation ([endpoints](https://www.recraft.ai/docs/api-reference/endpoints.md), [pricing](https://www.recraft.ai/docs/api-reference/pricing.md)). But that is *style* transfer, and Recraft documents character consistency as its own separate topic ([Character consistency](https://www.recraft.ai/docs/best-practices/character-consistency.md)) — whether it holds *facial identity* at bust-crop scale is **UNVERIFIED**.

### 1.3 Flux + LayerDiffuse / latent transparency

**The original work.** *"Transparent Image Layer Diffusion using Latent Transparency"*, Lvmin Zhang & Maneesh Agrawala, arXiv:2402.17113, 2024-02-27 ([abstract](https://arxiv.org/abs/2402.17113)). The method *"learns a 'latent transparency' that encodes alpha channel transparency into the latent manifold of a pretrained latent diffusion model"*, trained on *"1M transparent image layer pairs"*. The paper's headline result is exactly our question:

> *"A user study finds that in most cases (97%) users prefer our natively generated transparent content over previous ad-hoc solutions such as generating and then matting."*
> — [arXiv:2402.17113](https://arxiv.org/abs/2402.17113)

That is a direct, primary-source argument against our current architecture — *if* a hosted endpoint existed.

**Entry point repo:** [lllyasviel/LayerDiffuse](https://github.com/lllyasviel/LayerDiffuse) (Apache-2.0). It is a signpost only — three files, no code. It points to [sd-forge-layerdiffuse](https://github.com/layerdiffusion/sd-forge-layerdiffuse) (a WebUI extension) and [LayerDiffuse_DiffusersCLI](https://github.com/lllyasviel/LayerDiffuse_DiffusersCLI) (Apache-2.0, "pure diffusers without any GUI", self-described **Work In Progress**). The README roadmap still lists "Gradio + Diffusers + Colab: **Coming soon**" and "Huggingface Space: **Coming soon**".

**Straight or premultiplied? Straight.** From the reference decoder, [`lib_layerdiffuse/vae.py`](https://github.com/lllyasviel/LayerDiffuse_DiffusersCLI/blob/main/lib_layerdiffuse/vae.py):

```python
alpha = y[..., :1]
fg = y[..., 1:]
...
vis = (fg * alpha + cb * (1 - alpha))[0]      # composite for display: fg is NOT pre-scaled by alpha
...
png = torch.cat([fg, alpha], dim=3)[0]        # written out as [RGB, A] with raw fg
```

`fg` is multiplied by `alpha` only at *composite* time, and the saved PNG concatenates the un-scaled `fg` with `alpha`. That is **straight (unassociated) alpha**. Note also `pad_rgb()` in the same file — *"The algorithm to convert a transparent PNG image to a 'padded' image with all invisible pixels filled with smooth, continuous colors. This padded RGB format is used for the training of all LayerDiffuse models"* ([README](https://github.com/lllyasviel/LayerDiffuse_DiffusersCLI)) — the model is trained to produce meaningful colour *underneath* transparent pixels, which is precisely what kills halos on downscale (§4.2). The encoder does use a premultiplied feed internally (`vae_feed = (rgb_bchw_01 * 2.0 - 1.0) * a_bchw_01`), but that is an internal detail, not the output contract.

**Hosted endpoint:** none found. A keyword search of fal's model catalogue for `transparent` returns 8 models (§1.6) and **no LayerDiffuse endpoint** ([fal catalogue query](https://fal.ai/api/models?keywords=transparent)). LayerDiffuse is SDXL/SD1.5-era; a Flux-family port with a hosted API is **UNVERIFIED** — we found none. Self-hosting is possible (Apache-2.0, "you only need 8GB Nvidia VRAM") but that is an infra project, not an API swap, and the SDXL base has no Kontext-grade reference-identity conditioning.

### 1.4 Ideogram V3 Transparent — real native alpha, zero identity

fal hosts `fal-ai/ideogram/v3/generate-transparent`, *"Generate images with transparent backgrounds using Ideogram Transparent model"*, priced **\$0.03 TURBO / \$0.06 BALANCED / \$0.09 QUALITY** ([model card](https://fal.ai/models/fal-ai/ideogram/v3/generate-transparent)). Example output is a `.png`.

Its **category is `text-to-image`** and its complete input schema is `prompt`, `aspect_ratio`, `rendering_speed`, `expand_prompt`, `negative_prompt`, `num_images`, `seed`, `sync_mode` — **there is no image input of any kind** ([model card](https://fal.ai/models/fal-ai/ideogram/v3/generate-transparent)). No `image_url`, no character reference, no style reference. It cannot hold our character.

Ideogram *does* ship character consistency — as three *separate*, *non-transparent*, `image-to-image` endpoints: `fal-ai/ideogram/character`, `/character/edit`, `/character/remix`, all **\$0.10 TURBO / \$0.15 BALANCED / \$0.20 QUALITY** ([fal catalogue](https://fal.ai/api/models?keywords=character)). Transparency and identity are in different products. Note also `expand_prompt` (MagicPrompt) defaults to **`true`** on the transparent endpoint — i.e. prompt rewriting on by default, which we would have to disable.

### 1.5 FLUX.1 Kontext [pro] — what we actually use

`fal-ai/flux-pro/kontext`, **\$0.04 per image**, category `image-to-image`, required inputs `prompt` and `image_url` (*"Image prompt for the omni model"*), `output_format` enum `jpeg | png` (default `jpeg`) ([model card](https://fal.ai/models/fal-ai/flux-pro/kontext)). **There is no `background` or transparency parameter in the schema.** It also exposes `safety_tolerance` — *"1 being the most strict and 5 being the most permissive"*, with enum values `"1"…"6"` — and `enhance_prompt` defaulting to `false` (same model card). That combination — a permissiveness dial and prompt-rewriting off by default — is why we are on fal, and it is worth protecting.

### 1.6 Everything else fal advertises as transparency-capable

A catalogue query for `transparent` returns exactly 8 models ([fal.ai/api/models?keywords=transparent](https://fal.ai/api/models?keywords=transparent)):

| Endpoint | Category | Native alpha at generation? | Ref-image identity? | Price (fal) |
|---|---|---|---|---|
| `fal-ai/ideogram/v3/generate-transparent` | text-to-image | **yes** | **no** (no image input) | \$0.03 / \$0.06 / \$0.09 |
| `bytedance/seedream/v5/pro/layerize` | image-to-image | no — decomposes an existing image | n/a (post-step) | \$0.03375/layer (<1536²), \$0.0675/layer above |
| `bria/extract-object` | image-to-image | no — segments, returns RGBA PNG | n/a (post-step) | \$0.02/image |
| `fal-ai/ideogram/remove-background` | image-to-image | no | n/a | \$0.01 |
| `fal-ai/imageutils/rembg` | image-to-image | no | n/a | GPU-A6000, per-second |
| `smoretalk-ai/rembg-enhance` | image-to-image | no | n/a | serverless, per-second |
| `topaz/upscale/image/transparent` | image-to-image | no — *preserves* existing alpha | n/a | \$0.08 per started 24MP output |
| `bria/fibo-lite/.../structured_prompt` | text-to-json | no ("transparent" = interpretability) | n/a | — |

**No fal-hosted model both accepts a reference image for identity and emits alpha natively.** That is a complete enumeration of fal's own transparency-tagged catalogue, not a sample.

Two are worth a second look:

- **Seedream 5.0 Pro Layerize** — *"Splits a finished image into independent, editable transparent-PNG layers … returning 2 to 17 layers per call"* ([model card](https://fal.ai/models/bytedance/seedream/v5/pro/layerize)). As a subject/background splitter for a bust it is at least \$0.0675 (2 layers minimum) — more expensive than any dedicated matter, with unbounded layer-count risk. `enable_safety_checker` defaults `true` and *"Disabling it requires account authorization"*.
- **Topaz Upscale Image Transparent** — *"Preserves the alpha channel end to end with PNG output"* ([catalogue](https://fal.ai/api/models?keywords=transparent)). Irrelevant to generation, but it is the one primary source confirming that alpha-preserving *resampling* is treated as a distinct, non-trivial product — a hint that §4.2 is a real failure mode, not a theoretical one.

### 1.7 Stability / SDXL

Base SDXL emits 3-channel RGB; transparency for SDXL exists only via the LayerDiffuse latent-transparency finetune (§1.3), which has no hosted endpoint. No Stability-hosted native-alpha endpoint appears in fal's transparency catalogue ([query](https://fal.ai/api/models?keywords=transparent)). A Stability-native alpha API is **UNVERIFIED** — we did not find one and fal does not host one.

---

## 2. Background removal / matting — two-call candidates

### 2.1 BiRefNet (what we use)

**Paper:** *"Bilateral Reference for High-Resolution Dichotomous Image Segmentation"*, CAAI AIR 2024, [arXiv:2401.03407](https://arxiv.org/pdf/2401.03407).
**Repo:** [ZhengPeng7/BiRefNet](https://github.com/ZhengPeng7/BiRefNet) — **MIT licence**, 4.1k stars. Commercially unrestricted.

**Matting on hair.** The repo's model zoo distinguishes *segmentation* weights from *matting* weights, and publishes metrics ([README, model zoo](https://github.com/ZhengPeng7/BiRefNet)):

| Weight | Training data | Test set | Metric (S, wF) |
|---|---|---|---|
| **portrait matting** | P3M-10k, TR-humans | P3M-500-P | 0.983, 0.989 |
| **general matting** | P3M-10k, AM-2k, AIM-500, HIM2K, PPM-100, Distinctions-646, … | TE-P3M-500-NP | 0.979, 0.988 |
| general use (2048²) | DIS/HRSOD/UHRSD/P3M/… | DIS-VD | 0.927, 0.894 |

P3M is a *portrait matting* benchmark, so the "portrait" weights are trained on exactly the soft-hair-edge case. The repo also released [BiRefNet-matting](https://huggingface.co/ZhengPeng7/BiRefNet-matting) ("general trimap-free matting", Oct 2024), [BiRefNet_HR-matting](https://huggingface.co/ZhengPeng7/BiRefNet_HR-matting) (2048², Feb 2025), and [BiRefNet_dynamic](https://huggingface.co/ZhengPeng7/BiRefNet_dynamic) (256²–2304², Mar 2025).

**Halo mitigation is a named feature.** BiRefNet ships `refine_foreground` — foreground colour estimation that recovers un-contaminated RGB under semi-transparent pixels. The repo logs it as a hot path they optimised: *"We managed to accelerate refine_foreground by 8 times (~80ms now on 5090)"* (2025-06-30) ([README news](https://github.com/ZhengPeng7/BiRefNet)). fal exposes it as `refine_foreground: boolean`, *"Whether to refine the foreground using the estimated mask"*, **default `true`** ([fal model card](https://fal.ai/models/fal-ai/birefnet)). We do not set it, so we get the default — which is the correct one. Good.

**Latency:** 86.8ms FP32 / 69.4ms FP16 on A100; 95.8ms / 57.7ms on a 4090; *"17 FPS at 1024x1024 with 3.45GB GPU memory on a single RTX 4090"* in FP16 with *"~0 decrease of performance"* ([README, efficiency](https://github.com/ZhengPeng7/BiRefNet)). Sub-100ms model time — our 60s client timeout is dominated by network and the base64 data-URI upload, not inference.

**We are leaving capability on the table.** `fal-ai/birefnet/v2` exposes a superset: a `"Matting"` model option (`BiRefNet-matting`), `"General Use (Dynamic)"`, an added `2304x2304` operating resolution, and `mask_only` ([v2 model card](https://fal.ai/models/fal-ai/birefnet/v2)). The BiRefNet author points at v2 as the current inference-partner endpoint ([README, Inference Partner](https://github.com/ZhengPeng7/BiRefNet)). We call v1 with `"Portrait"` at the default `1024x1024`, i.e. an older endpoint at the lowest operating resolution.

**Price: fal's model card renders "\$0 per compute seconds"** — an unpopulated template field, on both [v1](https://fal.ai/models/fal-ai/birefnet) and [v2](https://fal.ai/models/fal-ai/birefnet/v2). fal's [pricing page](https://fal.ai/pricing) publishes per-image prices only for four generative models (Seedream V4 \$0.03, Flux Kontext Pro \$0.04, Nanobanana \$0.0398, Qwen \$0.02/MP) and notes *"Some other models may use GPU-based pricing depending on architecture"*, with GPU list prices from \$2.99/h (RTX PRO 6000) to \$8.50/h (B300). **Our BiRefNet unit cost is UNVERIFIED from primary sources** — fal does not publish it. That is precisely the accounting gap this note was commissioned around, and it is a vendor-side documentation gap, not something we can close by reading. Only a metered test call can close it.

### 2.2 The alternatives

| Option | Licence | Hair / soft-edge evidence | Price | Latency |
|---|---|---|---|---|
| **BiRefNet** (ours) | [MIT](https://github.com/ZhengPeng7/BiRefNet) | portrait-matting weights, P3M-500-P S=0.983 wF=0.989; `refine_foreground` | fal: **UNVERIFIED** ("\$0 per compute seconds") | 57–95ms on 4090 ([README](https://github.com/ZhengPeng7/BiRefNet)) |
| **BRIA RMBG-2.0** | **`license: other`, gated access** ([HF model card](https://huggingface.co/briaai/RMBG-2.0)) | vendor self-tags the model `legal liability` (same card) | — | — |
| **BRIA Extract Object** (fal) | fal marks `licenseType: commercial` | *"return it as an RGBA PNG"*; `remove_background` flag *"refine the cutout alpha with background removal (RMBG)"*, else raw SAM mask | **\$0.02/image** ([model card](https://fal.ai/models/bria/extract-object)) | — |
| **rembg** | **[MIT](https://github.com/danielgatis/rembg)** | ships BiRefNet portrait/general/DIS + BRIA RMBG + SAM + u2net sessions; has `-a` alpha matting and `-dc` *"Remove color fringing from soft edges"* ([README](https://github.com/danielgatis/rembg)) | self-host; on fal `fal-ai/imageutils/rembg` GPU-A6000 per-second | — |
| **SAM / SAM-family** | via `rembg/sessions/sam.py` ([repo](https://github.com/danielgatis/rembg)) | prompt-driven **segmentation**, binary masks — not matting. Bria's own copy claims Extract Object "outperform[s] SAM 3.1" for commercial extraction ([fal](https://fal.ai/api/models?keywords=transparent)) | — | — |
| **Ideogram Remove Background** (fal) | `commercial` | none published | **\$0.01** ([fal catalogue](https://fal.ai/api/models?keywords=transparent)) | — |
| **remove.bg** | Kaleido/Canva ToS | *"Requires images that have a foreground (e.g. people, products, animals, cars…)"*; output up to 50MP; single `POST /v1.0/removebg` ([API docs](https://www.remove.bg/api)) | *"Your first 50 API calls per month are on us"*; per-credit price **UNVERIFIED** (pricing page is client-rendered) | — |
| **Photoroom** | — | rembg's listed sponsor ([rembg README](https://github.com/danielgatis/rembg)) | **UNVERIFIED** — [pricing page](https://www.photoroom.com/api/pricing) serves only a volume slider and tier names (Basic / Plus / Enterprise, "1,000 free images" sandbox, Enterprise "200K images minimum"); no numeric per-image price | — |
| **ClipDrop** | — | **UNVERIFIED** — not investigated to primary source | **UNVERIFIED** | — |

**Reading of that table.** BiRefNet is not a compromise we settled for; it is the model the *other* products are built on. rembg's session list is literally `birefnet_portrait.py`, `birefnet_general.py`, `birefnet_massive.py`, `bria_rmbg.py`, `sam.py`, `u2net.py` ([repo tree](https://github.com/danielgatis/rembg)) — switching to rembg means running BiRefNet ourselves. BRIA's own fal endpoint uses RMBG only as an optional *refinement* on top of a SAM mask ([Extract Object](https://fal.ai/models/bria/extract-object)). And RMBG-2.0 is **gated with `license: other` and self-tagged `legal liability`** ([HF card](https://huggingface.co/briaai/RMBG-2.0)) — against MIT-licensed BiRefNet, that is a licence downgrade for no quality argument we can source.

**There is no matting upgrade available to us.** There is only a *configuration* upgrade: v2 + `"Matting"` + higher operating resolution.

---

## 3. The decisive question

> Can any SINGLE call give us BOTH reference-image identity conditioning AND true alpha?

### **YES — but only outside fal, and at a censorship cost we have said we will not pay.**

**The yes.** OpenAI `POST /v1/images/edits` accepts up to 16 reference images *and* `background: "transparent"` in the same request body. Both are documented on the same endpoint, in the same parameter list ([Images API reference](https://developers.openai.com/api/reference/resources/images)). Exact shape:

```http
POST https://api.openai.com/v1/images/edits
Authorization: Bearer $OPENAI_API_KEY
Content-Type: application/json

{
  "model": "gpt-image-2",
  "images": [{ "image_url": "data:image/png;base64,<ANCHOR>" }],
  "prompt": "<emotion cell prompt>",
  "background": "transparent",
  "output_format": "png",
  "size": "1024x1024",
  "quality": "medium"
}
```

Constraints, all from the reference: `jpeg` is rejected with `transparent`; `input_fidelity` must be omitted for `gpt-image-2` (always high); transparency on `gpt-image-2` is flagged **preview**.

**The no, for us.** Three independent reasons, each sourced:

1. **Moderation has no off switch and fires on output.** `moderation` accepts only `"low" | "auto"`; failures surface as `moderation_blocked` with `moderation_stage: "input" | "output"` ([API reference](https://developers.openai.com/api/reference/resources/images), [guide](https://developers.openai.com/api/docs/guides/image-generation)). An output-stage block means we pay for a render we never receive, and a character pack can fail *per cell* for reasons we cannot configure away. This is a direct conflict with the NO CENSORSHIP goal — it is not a tuning knob like fal's `safety_tolerance: "1"…"6"` ([Kontext](https://fal.ai/models/fal-ai/flux-pro/kontext)).
2. **Preview status.** OpenAI labels transparent backgrounds on `gpt-image-2` as *"in preview"* ([API reference](https://developers.openai.com/api/reference/resources/images)). Our most expensive path should not sit on a preview flag.
3. **A second provider.** New vendor, new auth, new billing, plus mandatory [org verification](https://help.openai.com/en/articles/10910291-api-organization-verification) ([guide](https://developers.openai.com/api/docs/guides/image-generation)).

**On fal specifically, the answer is an unambiguous NO**, and this is now enumerated rather than assumed: of the 8 transparency-capable endpoints fal hosts ([catalogue](https://fal.ai/api/models?keywords=transparent)), the only one with native generative alpha — `fal-ai/ideogram/v3/generate-transparent` — is `text-to-image` **with no image input field at all**, and every other one is a post-hoc `image-to-image` step. Conversely, every identity-capable endpoint (Kontext, Ideogram Character ×3, Instant Character) has no `background`/alpha parameter in its schema ([Kontext](https://fal.ai/models/fal-ai/flux-pro/kontext), [character catalogue](https://fal.ai/api/models?keywords=character)).

### The technical reason the two rarely coexist

Reference-image identity conditioning and latent transparency are **competing modifications of the same latent space**. LayerDiffuse's whole contribution is *"regulating the added transparency as a latent offset with minimal changes to the original latent distribution of the pretrained model"* — it must be minimal precisely because perturbing the latent manifold degrades the base model ([arXiv:2402.17113](https://arxiv.org/abs/2402.17113)). Reference conditioning (Kontext-style) is a *second* perturbation of that same manifold. Stacking them means retraining the transparency VAE against the conditioned model, on paired transparent data — and the paper notes the alpha training set had to be 1M pairs *"collected using a human-in-the-loop collection scheme"*. There is no cheap composition.

The only architectures that dodge this are end-to-end multimodal ones that emit pixels autoregressively rather than through a latent-diffusion VAE — which is exactly the family GPT Image belongs to (*"GPT Image models prior to `gpt-image-2` generate images by first producing specialized image tokens"*, [guide](https://developers.openai.com/api/docs/guides/image-generation)). That is not a coincidence: it is why OpenAI is the single vendor that can offer both in one call.

**This verdict justifies keeping the 2-call design on fal.** It is now evidenced, not assumed.

---

## 4. Alpha correctness pitfalls

### 4.1 Straight vs premultiplied — and why the boundary is at our PNG decode

The PNG specification is unambiguous:

> *"The color values in a pixel are **not premultiplied** by the alpha value assigned to the pixel. This rule is sometimes called 'unassociated' or 'non-premultiplied' alpha."*
> — [PNG Specification (Third Edition), W3C REC 2025-06-24](https://www.w3.org/TR/png-3/)

and it gives the conversion:

> *"If the original image has premultiplied (also called 'associated') alpha data, it can be converted to PNG's non-premultiplied format by dividing each sample value by the corresponding alpha value, then multiplying by the maximum value…"* — [ibid.](https://www.w3.org/TR/png-3/)

Go is the exact opposite:

> *"RGBA represents a traditional 32-bit **alpha-premultiplied** color… An alpha-premultiplied color component C has been scaled by alpha (A), so has **valid values 0 <= C <= A**."*
> — `go doc image/color.RGBA`, Go stdlib ([image/color](https://pkg.go.dev/image/color#RGBA))

**What each candidate returns:**

| Source | Alpha convention | Evidence |
|---|---|---|
| fal BiRefNet (PNG) | **straight** | `output_format: png` ⇒ PNG rules ([model card](https://fal.ai/models/fal-ai/birefnet), [PNG spec](https://www.w3.org/TR/png-3/)) |
| OpenAI `background:transparent` + `output_format:png` | **straight** | PNG by format definition ([API ref](https://developers.openai.com/api/reference/resources/images), [PNG spec](https://www.w3.org/TR/png-3/)) |
| OpenAI `output_format:webp` | **UNVERIFIED** — WebP ALPH convention not confirmed for their encoder | [WebP container spec](https://developers.google.com/speed/webp/docs/riff_container) |
| LayerDiffuse | **straight** | `png = torch.cat([fg, alpha])` with un-scaled `fg` ([vae.py](https://github.com/lllyasviel/LayerDiffuse_DiffusersCLI/blob/main/lib_layerdiffuse/vae.py)) |
| Bria Extract Object | straight (RGBA PNG) | *"return it as an RGBA PNG"* ([model card](https://fal.ai/models/bria/extract-object)) |
| Go `image.RGBA` in our process | **premultiplied** | `go doc image/color.RGBA` |

The conversion happens inside `image.Decode`: Go's PNG decoder yields `*image.NRGBA` for 8-bit RGBA PNGs (N = non-premultiplied, the PNG convention), and premultiplication occurs when that is drawn into an `*image.RGBA`. Our code never touches raw channel bytes, so we never straddle the boundary by hand.

### 4.2 What happens to alpha when we resize — we are correct, by inheritance rather than by intent

`internal/imaging/imaging.go` does:

```go
dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, b, xdraw.Over, nil)
```

**The failure mode this avoids.** Resampling is a weighted average of neighbouring pixels. If you average *straight* (unassociated) RGB across an edge, the fully-transparent pixels contribute their RGB — which in a matted cutout is usually black (0,0,0) or white — into the colour of the semi-transparent hair pixels next to them. Alpha stays right; colour goes wrong. Result: a **dark halo** (black-backed cutouts) or a **white fringe** (white-backed) tracing every strand of hair. The average is only mathematically valid on **premultiplied** values, because premultiplied colour already encodes "this pixel contributes nothing" as (0,0,0,0), so a transparent neighbour contributes zero to both numerator and denominator.

**Why we are safe.** `x/image/draw` composites in Go's premultiplied space. From `golang.org/x/image@v0.42.0/draw/scale.go`, the `Over` path is textbook premultiplied source-over:

```go
pa1 := 0xffff - pa
dstColorRGBA64.R = uint16(qr*pa1/0xffff + pr)   // dst*(1-Asrc) + src   -- premultiplied
dstColorRGBA64.G = uint16(qg*pa1/0xffff + pg)
dstColorRGBA64.B = uint16(qb*pa1/0xffff + pb)
dstColorRGBA64.A = uint16(qa*pa1/0xffff + pa)
```

There is no `C/A` un-premultiply anywhere in the scaling path, and `ftou`/`fffftou` clamp results to `[0, 0xffff]` (`if i > 0xffff { return 0xffff }`). Since `dst` is a freshly-allocated (and therefore fully transparent) `image.RGBA`, `Over` reduces to `Src` and the maths is exact.

**Two caveats worth writing down:**

1. `CatmullRom` is a *negative-lobe* kernel — it overshoots at high-contrast edges. Clamping to `[0, 0xffff]` is applied per channel independently (`scale.go`, `ftou`), so an overshoot can in principle produce `C > A`, violating `image/color.RGBA`'s stated invariant (*"valid values 0 <= C <= A"* — `go doc image/color.RGBA`). In practice this shows up as a faint bright rim on very high-contrast alpha edges. Severity on our sprites is **UNVERIFIED** — it is measurable (assert `R,G,B <= A` on every pixel of a downscaled tier) and worth measuring before it is worried about.
2. The comment in `background.go` — *"EncodeTiers already preserves alpha through its RGBA resize"* — is **correct**, and now has a citation behind it.

### 4.3 Format support for alpha

| Format | Alpha | Notes |
|---|---|---|
| **PNG** | yes, 8/16-bit, **non-premultiplied** | [PNG spec](https://www.w3.org/TR/png-3/). What we store. Lossless, universally decodable. |
| **WebP** | yes | *"Transparency: An image may have transparency, that is, an alpha channel."* Lossless (`VP8L`) carries alpha in the simple file format; lossy alpha requires the **extended** format (`VP8X` + `ALPH` chunk) ([WebP Container Specification](https://developers.google.com/speed/webp/docs/riff_container)) — and note *"Older readers may not support files using the lossless format"* (ibid.). OpenAI accepts `webp` with `background:transparent` ([API ref](https://developers.openai.com/api/reference/resources/images)); fal BiRefNet offers `webp` output ([model card](https://fal.ai/models/fal-ai/birefnet)). |
| **AVIF** | yes (auxiliary alpha item) | Not investigated to primary source here — **UNVERIFIED** for our purposes. |
| **JPEG** | **no** | Confirmed operationally by OpenAI rejecting it: *"`jpeg` isn't supported with transparent backgrounds"* ([guide](https://developers.openai.com/api/docs/guides/image-generation)). |
| **GIF** | 1-bit only | fal BiRefNet offers `gif` output ([model card](https://fal.ai/models/fal-ai/birefnet)) — useless for hair. |

**For game sprites, stay on PNG.** It is the only one of these with a spec-mandated, unambiguous, non-premultiplied 8-bit alpha and no decoder-support caveat. The one real cost is size — note that the data URI we send to BiRefNet is base64 (+33%) of a full-resolution PNG, per cell. Specific browser/engine constraints for our target runtime are **UNVERIFIED** (not investigated).

### 4.4 Matting halos on hair, and how vendors mitigate them

Two distinct artifacts, often conflated:

1. **Colour contamination / fringing** — semi-transparent hair pixels retain RGB blended with the *old* background. Mitigation is **foreground colour estimation**: solve for un-contaminated `F` given the observed `C = aF + (1-a)B` and the estimated `a`. BiRefNet's `refine_foreground` is exactly this and is **on by default** on fal ([model card](https://fal.ai/models/fal-ai/birefnet)); the author treats it as a first-class hot path ([README news, 2025-06-30](https://github.com/ZhengPeng7/BiRefNet)). rembg exposes the same idea as `-dc`, *"Remove color fringing from soft edges"* ([README](https://github.com/danielgatis/rembg)).
2. **Hard-mask aliasing** — a binary segmentation mask has no soft alpha at all, so hair becomes a jagged silhouette. This is the SAM failure mode, and Bria names it in their own schema: `remove_background` *"When True, refine the cutout alpha with background removal (RMBG). When False (default), use the SAM segmentation mask as the cutout's alpha -- faster, with **no salient-object matting**"* ([Extract Object](https://fal.ai/models/bria/extract-object)). Segmentation is not matting.

**Native generation sidesteps both**, because there is no background to be contaminated by, and no mask to threshold. That is the mechanism behind the paper's 97% preference for native alpha over generate-then-matte ([arXiv:2402.17113](https://arxiv.org/abs/2402.17113)) — and it is also why the LayerDiffuse authors bothered to invent `pad_rgb`, filling invisible pixels with *"smooth, continuous colors"* so that resampling a native-alpha image never pulls garbage in from under the transparency ([DiffusersCLI README](https://github.com/lllyasviel/LayerDiffuse_DiffusersCLI)).

---

## 5. Recommendation

### Verdict: **stay 2-call.** Do not chase a native-alpha single call. Do fix the accounting and upgrade the removal *configuration*.

The 2-call design is not a workaround we tolerate — for a fal-hosted, uncensored, identity-conditioned pipeline it is the **only** design available (§3), and the second call is the best-in-class matter that the competing products are themselves built on (§2.1).

### Cost comparison

| Architecture | Call 1 | Call 2 | Cost per usable sprite | Identity | Censorship posture |
|---|---|---|---|---|---|
| **Current: Kontext + BiRefNet** | \$0.0400 [1] | **UNVERIFIED** [2] | **\$0.0400 + X** | yes (Kontext ref) | `safety_tolerance` 1–6, `enhance_prompt:false` [1] |
| Kontext + Ideogram Remove BG | \$0.0400 [1] | \$0.0100 [3] | **\$0.0500** | yes | fal-native |
| Kontext + Bria Extract Object | \$0.0400 [1] | \$0.0200 [4] | **\$0.0600** | yes | fal-native |
| Kontext + Seedream Layerize | \$0.0400 [1] | ≥\$0.0675 (2-layer min) [5] | **≥\$0.1075** | yes | safety checker on by default [5] |
| Recraft V3 + Recraft removeBackground | \$0.0400 [6] | \$0.0100 [6] | **\$0.0500** | style refs only | — |
| **gpt-image-2 edits, 1 call, medium 1024²** | \$0.0530 + input tokens [7] | — | **≥\$0.0530** | yes (≤16 refs, forced high fidelity) | `moderation` low/auto only; output-stage blocks [8] |
| gpt-image-2 edits, 1 call, low 1024² | \$0.0060 + input tokens [7] | — | **≥\$0.0060** | yes | same [8] |
| gpt-image-1-mini edits, medium 1024² | \$0.0110 + input tokens [7] | — | **≥\$0.0110** | transparency support **UNVERIFIED** for mini | same [8] |
| Ideogram Transparent (1 call, native alpha) | \$0.0600 BALANCED [9] | — | **\$0.0600** | **NONE — no image input** | `expand_prompt:true` by default [9] |

[1] https://fal.ai/models/fal-ai/flux-pro/kontext ·
[2] https://fal.ai/models/fal-ai/birefnet renders "\$0 per compute seconds"; https://fal.ai/pricing does not list it ·
[3] https://fal.ai/api/models?keywords=transparent ·
[4] https://fal.ai/models/bria/extract-object ·
[5] https://fal.ai/models/bytedance/seedream/v5/pro/layerize ·
[6] https://www.recraft.ai/docs/api-reference/pricing.md ·
[7] https://developers.openai.com/api/docs/pricing#image-generation and https://developers.openai.com/api/docs/guides/image-generation ·
[8] https://developers.openai.com/api/reference/resources/images ·
[9] https://fal.ai/models/fal-ai/ideogram/v3/generate-transparent

**The cost headline.** Even taking the *most favourable* published bound for the single-call option — gpt-image-2 at `medium` — it is **\$0.053 + input tokens vs \$0.040 + X**. The single call is only cheaper if BiRefNet costs us more than ~\$0.013/call *and* the reference-image input tokens are negligible. Neither is established. The 2-call architecture is not obviously the expensive option; we have simply never measured X.

**What actually reduces spend is not the architecture.** It is:

- **X is unmeasured and unbilled.** Measure it before optimising it. If X turns out to be much greater than \$0.01, swapping BiRefNet for `fal-ai/ideogram/remove-background` at a *published* \$0.01 makes the whole path cost-truthful at a known \$0.05 — the value there is the published price, not the saving.
- **We send full-resolution base64 data URIs** (`background.go`, +33% over the wire, per cell). If fal bills BiRefNet by compute-seconds ([pricing page](https://fal.ai/pricing) GPU rates), upload and decode time is billable time.

### Do this instead (all config, no architecture change)

1. **Move to `fal-ai/birefnet/v2` and evaluate `model: "Matting"`.** The v2 endpoint adds a purpose-built `BiRefNet-matting` option that v1 does not have; the author points at v2 as the current partner endpoint ([v2 card](https://fal.ai/models/fal-ai/birefnet/v2), [README](https://github.com/ZhengPeng7/BiRefNet)). We currently call v1 with `"Portrait"`. The tradeoff is real, not free: `"Portrait"` is trained on P3M-10k portraits (S=0.983, wF=0.989 on P3M-500-P) and our subjects *are* busts — this needs an A/B, not an assumption.
2. **Consider `operating_resolution: "2048x2048"`** for hair detail ([v2 card](https://fal.ai/models/fal-ai/birefnet/v2)) — and price the compute-second delta once X is known.
3. **Keep `refine_foreground` at its default `true`** — it is the anti-fringing mechanism (§4.4).
4. **Set `output_format: "png"` on the Kontext call.** It defaults to `jpeg` ([Kontext card](https://fal.ai/models/fal-ai/flux-pro/kontext)), so we currently JPEG-compress the image *before* matting it — lossy ringing at exactly the high-frequency hair edges the matter must resolve. **This is the cheapest quality win in the whole document and it costs nothing.**
5. **Do not adopt gpt-image-2** for character packs. Output-stage `moderation_blocked` with no opt-out ([guide](https://developers.openai.com/api/docs/guides/image-generation)) is disqualifying under the NO CENSORSHIP goal, and transparency there is preview-flagged.

### Hybrid: worth exactly one experiment, not a migration

If a pack cell needs **no** identity (props, effects, UI ornaments), `fal-ai/ideogram/v3/generate-transparent` at \$0.03 TURBO gives native alpha in one call with no matting artifacts at all ([model card](https://fal.ai/models/fal-ai/ideogram/v3/generate-transparent)) — set `expand_prompt: false` to stop MagicPrompt rewriting. For character cells it is not a candidate: no image input exists.

### What must be TESTED to confirm this

Extend the existing benchmark-harness convention at `cmd/sprite-sheet-benchmark`:

1. **Measure X.** Run N BiRefNet calls with billing instrumented. This is the one number the whole cost argument turns on and it is unobtainable from fal's docs (§2.1). Everything else is secondary.
2. **JPEG-vs-PNG intermediate.** Same anchor, same prompt, same seed; Kontext `output_format` `jpeg` vs `png`; both through BiRefNet. Compare alpha edge quality on hair. Expect a visible win for `png` (#4 above); if not, that assumption dies cheaply.
3. **v1/Portrait vs v2/Matting vs v2/Portrait@2048.** Same input set. Score alpha-edge quality on hair, plus wall-clock and cost delta.
4. **Alpha-invariant assertion on tiers.** After `EncodeTiers`, assert `R,G,B <= A` for every pixel of `Preview` and `Thumb` (§4.2 caveat 1). A failure count > 0 confirms CatmullRom overshoot is real for our content; zero closes the question permanently.
5. **Halo regression fixture.** Composite each tier over pure black *and* pure white. A dark or bright rim visible on one but not the other is fringing; a rim on both is overshoot. This distinguishes the two failure modes of §4.4 unambiguously.
6. **Identity-vs-alpha ceiling (optional, informational).** If we ever revisit the verdict: one gpt-image-2 `/images/edits` call with our anchor and `background:transparent`, scored for identity retention against the Kontext+BiRefNet output — and logging how many of N cells return `moderation_blocked`. That block rate is the real decision variable, not image quality.

---

## Appendix: explicitly UNVERIFIED

- fal's per-call price for `fal-ai/birefnet` (both model cards render an unpopulated "\$0 per compute seconds").
- gpt-image-2 input-token count for a 1024×1024 reference image at forced-high fidelity.
- Whether `gpt-image-1-mini` is a "supported GPT Image model" for `background: transparent`.
- Alpha convention (straight/premultiplied) of OpenAI's `webp` transparent output.
- remove.bg per-credit price; Photoroom per-image price (both pricing pages are client-rendered and serve no numbers to a fetch).
- ClipDrop entirely — not investigated to a primary source.
- AVIF alpha specifics, and browser/engine constraints for our specific sprite runtime.
- Whether Recraft's `style_references` hold *facial identity* (as opposed to style) at bust crop.
- Existence of any Flux-family LayerDiffuse port with a hosted endpoint (none found on fal).
- Severity of CatmullRom overshoot (`C > A`) on our actual sprite content.
