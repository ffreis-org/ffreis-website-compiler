# Agent Context

**This repo:** `ffreis-website-compiler` — Go CLI that builds and validates static
websites. Provides `cmd/website-compiler` (full CLI: build, serve, validate-*) and
`cmd/build-static` (CI-optimized build-only). Used by every website in the fleet,
both locally (via `ffreis-siteops`) and in CI/CD (via `ffreis-website-deployer`).

For the complete system map — how this repo relates to siteops, the deployer,
the inventory, and each website — see the private fleet inventory repository:

> `FelipeFuhr/ffreis-website-inventory` → `AGENTS.md`

Architecture detail (compiler layout detection in CI, command reference): `AGENTS.md`
links to `docs/ARCHITECTURE.md` in the same repo.

Do not look for cross-component flow documentation in this repo's README;
it covers only the compiler's own commands and flags.

## Template functions

The compiler registers these functions in `internal/sitegen/sitegen.go`:
- `dict(k, v, ...)` — builds a `map[string]any` from pairs
- `list(v, ...)` — builds a `[]any`
- `safeHTML(s)` — returns `template.HTML`, bypasses HTML escaping
- `toJSON(v)` — marshals any value to JSON, returns `template.JS` for `<script>` embedding
- `dig(root, keys...)` — safe nested key access with access-tracing for contract validation
- `required(v, msg)` — panics with msg if v is nil/zero
- `trimSuffix(s, suffix)` — wraps `strings.TrimSuffix`

## Automatic page transforms (`transformPage` in `internal/buildcmd/buildcmd.go`)

Every page produced by `build` / `build-static` passes through `transformPage`, which
applies four automatic transforms in order (no flags required):

### 1. Position-based CSS loading

Document position signals loading priority — mirroring the JS-at-end convention:

- **`<link rel="stylesheet">` in `<head>`** → inlined as a `<style>` block (critical path).
  Zero HTTP requests; page is fully styled from the first byte. `url()` refs (fonts,
  backgrounds) are rewritten to root-relative paths (`/fonts/file.woff2`) so they stay
  external and benefit from fingerprinting and long-lived caching.

- **`<link rel="stylesheet">` in `<body>`** → kept external, transformed to the deferred
  pattern:
  ```html
  <link rel="stylesheet" href="component.a1b2c3d4.css"
        media="print" onload="this.media='all'">
  <noscript><link rel="stylesheet" href="component.a1b2c3d4.css"></noscript>
  ```
  `media="print"` allows the browser to fetch without blocking rendering; `onload` applies
  the styles once ready. The `<noscript>` fallback handles JS-disabled environments. The
  href is fingerprinted by the next step.

**Convention for template authors:** place a stylesheet `<link>` in `<head>` if it affects
above-fold content (layout, header, typography). Place it in `<body>` if it can wait —
widgets below the fold, form validation, cookie banners, etc. No attribute or naming change
needed; position alone is the signal.

`<link media="screen and (...)">` in `<head>` are inlined as matching `<style media="...">`
blocks so responsive CSS continues to behave correctly.

When `-inline-assets` is set, full inlining (including data-URI fonts) is used for both
head and body instead, bypassing this step.

### 2. Navigation enhancement injection (progressive enhancement)

Two `<head>` elements are injected before `</head>` by `injectNavigationEnhancements`:

- **Cross-document view transitions** (`<style>@view-transition{navigation:auto}</style>`)
  — fades between pages instead of a hard repaint on same-origin navigation.
  Chrome 126+/Edge 126+; silently ignored elsewhere.

- **Speculation Rules** (`<script type="speculationrules">…</script>`)
  — prerenders same-origin pages on hover (`eagerness: "moderate"`), making navigation
  near-instant. Chrome 121+; silently ignored elsewhere.

These run on top of CSS inlining and serve as a progressive enhancement layer. They are
secondary to CSS inlining: even without them, FOUC is already eliminated by the inline CSS.

### 3. LQIP — blur-up placeholders for above-fold images (`lqip.go`)

For every `<img loading="eager" src="local.file">` (raster only — SVGs skipped):
- Decodes the image, scales to 20 px wide (nearest-neighbour), encodes as quality-20 JPEG.
- Replaces `src` with the base64 data URI (shows the blurry thumbnail immediately),
  moves the original path to `data-src`.
- Adds `class="lqip-pending"` (CSS: `filter: blur(8px); transition: filter .3s`).
- Injects one `<style>` block and one `<script>` block per page: the script swaps in
  the full image (from `data-src`) when it loads, removing the blur class.

Requires `golang.org/x/image/webp` for WebP decode. JPEG and PNG use stdlib.
Runs before fingerprinting so `data-src` gets fingerprinted to the hashed filename.

### 4. Asset fingerprinting (`fingerprint.go`)

Rewrites all local asset references to content-hashed filenames:
`portrait.webp` → `portrait.a1b2c3d4.webp` (SHA-256 of file, first 8 hex chars).

The packer (`ffreis-website-packer`) assigns `Cache-Control: immutable` (1 year) to
files whose names match `[._-][a-f0-9]{8,}[._-]`, so fingerprinted assets are
automatically cached long-term. Fingerprinting covers: `<img src>`, `<img data-src>`
(LQIP), `<link rel="preload" href>`, `<link rel="icon" href>` (also matches
`apple-touch-icon`), `<link rel="manifest" href>`, `<script src>`, and
`url()` inside inline `<style>` blocks. Data URIs and external URLs are left unchanged.

Hashed file copies are written to the output directory alongside the originals
(originals are also present but no longer referenced by any HTML).

### 5. External asset mirroring (flag: `-mirror-external-assets`)

Optional; downloads external CSS/JS/images and rewrites URLs to local copies.
Also processes `url()` references inside inline `<style>` blocks.

## Blog post processing (`-posts-dir` flag)

When `-posts-dir <path>` is passed to `build-static`, the compiler processes Markdown
blog posts in addition to the normal page build:

**Input layout expected in `<posts-dir>`:**
```
posts-dir/
  <slug>/
    index.md        # YAML frontmatter + Markdown body
    images/         # post images; copied to dist/blog/<slug>/images/
```

**What it generates:**
- `dist/blog/<slug>/index.html` — rendered post page (uses `src/templates/pages/post.gohtml`)
- `dist/blog/feed.xml` — RSS 2.0 feed for Substack import
- Replaces `pages.blog.posts` in site data with post metadata (injected after contract validation)

**Template data for post pages:** In addition to the standard `PageName` and `SiteData`,
post pages receive a `CurrentPost` map with: `title`, `date`, `summary`, `thumbnail`,
`canonical_url`, `tags`, `body_html`. The `post.gohtml` template accesses these via
`.CurrentPost`.

**Key packages:**
- `internal/posts/posts.go` — `LoadPostsDir`, `CopyPostImages`
- `internal/rss/rss.go` — `GenerateRSS`
- `internal/buildcmd/posts.go` — `injectPostsBlogList`, `writePostPages`, `writeRSSFeed`

**New dependency:** `github.com/yuin/goldmark` + `github.com/yuin/goldmark-meta`
for Markdown rendering and frontmatter parsing.

## Template extensibility blocks

Two `{{block}}` overrides in `head.gohtml` allow page templates to inject custom
meta tags without duplicating the head partial:

- `{{block "page_canonical" .}}` — overrides the `<meta name="description">` and
  `<link rel="canonical">` tags. The `post.gohtml` template overrides this to use
  `.CurrentPost` data instead of `pages.post.*` from site data.

- `{{block "page_og_meta" .}}` — overrides the full og:/twitter: meta block. The
  `post.gohtml` template overrides this with article-specific OG tags and structured data.

Both blocks receive `.` (the full template data map) as their pipeline.

## Keeping this file current

- **If you discover a fact not reflected here:** add it before finishing your task.
- **If something here is wrong or outdated:** correct it in the same commit as the code change.
- **If you rename a file, command, or concept referenced here:** update the reference.
