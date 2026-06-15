# ffreis-website-compiler

<!-- ffreis-badges:start -->
[![CI](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/FelipeFuhr/ffreis-badges/main/badges/ffreis-website-compiler/ci.json)](https://github.com/FelipeFuhr/ffreis-website-compiler/actions) [![License](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/FelipeFuhr/ffreis-badges/main/badges/ffreis-website-compiler/license.json)](https://github.com/FelipeFuhr/ffreis-website-compiler/blob/main/LICENSE)
<!-- ffreis-badges:end -->

A Go CLI that builds and validates the static websites in the ffreis fleet. It loads layered YAML site data, renders Go HTML templates, applies a pipeline of HTML/CSS/JS/image optimizations (CSS inlining, asset fingerprinting, LQIP placeholders, SVG/JS inlining, hreflang injection), and writes a deployable `dist/` tree. It also validates the site-data contract, asset reachability, and multilingual key parity. The same binary is used both locally (wrapped by `ffreis-siteops` for dev builds and `serve`) and in CI/CD (driven by the website deployer, which favors the build-only `build-static` entrypoint); keeping one compiler means local previews and deployed output are byte-for-byte identical.

## What it does

- **Builds** a static site from a website root laid out as `<root>/src/{assets,templates}` (legacy fallback `<root>/{site,templates}`), merging `site.d/*.yaml` data layers (shared + per-language) and rendering each page template.
- **Optimizes output** during the build: critical CSS inlined into `<head>` (minified, `@import` flattened), below-fold CSS deferred, small SVGs/JS/raster images inlined, LQIP blur-up placeholders for eager images, content-hash fingerprinting of all local assets, optional font embedding, optional external-asset mirroring, and progressive-enhancement hints (view transitions, speculation rules, script preloads).
- **Handles multilingual content** via `available_languages` per content item (renders or writes a redirect stub) and injects `<link rel="alternate" hreflang>` tags from `language_variants` site data.
- **Generates derived content**: Markdown blog posts (`-posts-dir`) into `dist/blog/<slug>/` plus an RSS feed, and paginated `/projects/` and `/courses/` listings from YAML data files.
- **Validates** the local site-data contract, that rendered pages only reference reachable local CSS/JS assets, baseline sanity invariants, and YAML key parity across language directories.
- **Serves** the site locally over HTTP for development with live template rendering and security headers.

## Usage

Install the main binary, or build it (see Development):

```bash
go install ./cmd/website-compiler
```

The CLI dispatches on a subcommand: `website-compiler <command> [flags]`.

```
build, compile        Build static website output
serve, web            Start a local development server
export-site-data      Export merged site data (incl. layers) as JSON/YAML
validate-site-data    Validate site data against the local site contract
validate-assets       Validate rendered pages only reference reachable local CSS/JS
validate-sanity       Run baseline sanity checks (contract + invariants + optional assets)
check-lang-parity     Check YAML key parity across multilingual data directories
help                  Show usage
```

### build

```bash
website-compiler build -website-root ../my-website -out dist
```

Common flags (defaults in parentheses):

| Flag | Description |
|---|---|
| `-website-root` (`.`) | Website project root (expects `src/{assets,templates}`) |
| `-out` (`dist`) | Output directory |
| `-assets-dir` / `-templates-dir` | Explicit source dirs (override `-website-root` resolution) |
| `-site-data` | Site-data source override (file/URL/directory of YAML layers) |
| `-copy-assets` (`true`) | Copy static assets into the output |
| `-inline-assets` (`false`) | Inline CSS/JS/images into each page for self-contained output |
| `-clean-urls` (`false`) | Emit `<name>/index.html` for extension-free URLs |
| `-js-inline-threshold` (`8192`) | Inline local `<script src>` below N bytes (0 disables) |
| `-raster-inline-threshold` (`0`) | Inline local raster `<img>` below N bytes (0 disables) |
| `-embed-fonts` (`false`) | Embed fonts in inlined CSS as base64 data URIs |
| `-mirror-external-assets` (`false`) | Download external CSS/JS/img/fonts and rewrite to local copies |
| `-posts-dir` | Markdown blog posts dir (`<slug>/index.md`); enables blog + RSS generation |
| `-projects-file` / `-courses-file` | YAML data files enabling paginated `/projects/` and `/courses/` |
| `-sanity` (`true`) | Fail the build if sanity checks fail |
| `-strict-contract` (`true`) | Fail if an allowed contract path is never referenced by a template |
| `-tracker-enabled` (`false`) | Inject the tracker SDK + `Tracker.init(...)`; needs `-tracker-sdk-version`, `-tracker-site-id`, `-tracker-endpoint` |

### serve

```bash
website-compiler serve -website-root ../my-website -addr :8080
```

Flags: `-website-root` (`.`), `-addr` (`:8080`), `-site-data`, `-sanity` (`true`). Server timeouts are overridable via `SERVE_*` env vars (e.g. `SERVE_READ_TIMEOUT`, `SERVE_SHUTDOWN_TIMEOUT`).

### Validation commands

```bash
website-compiler validate-site-data -website-root ../my-website
website-compiler validate-assets    -website-root ../my-website
website-compiler validate-sanity    -website-root ../my-website
```

All accept `-website-root`, `-templates-dir`, `-site-data` (and `validate-assets` also `-assets-dir`). `validate-sanity` adds `-check-assets` (`true`) and runs executable checks from `<sanity-dir>/checks.d/`.

### check-lang-parity

Standalone (no templates dir required); merges `site.d/*.yaml` per language, flattens keys, and reports structural drift. Exit `0` = clean, `1` = key mismatch.

```bash
website-compiler check-lang-parity -data-root ../my-website/data -langs en,pt
```

### Standalone entrypoints (`cmd/`)

Besides the full `website-compiler` CLI, single-purpose binaries are built from `cmd/` for CI use:

- `cmd/build-static` — build-only entrypoint (same flags as `build`); the CI/CD build path, also used by `make smoke-check`.
- `cmd/web` — `serve` only; `cmd/check-lang-parity` — `check-lang-parity` only; `cmd/ffreis-website-compiler` — legacy alias of the full CLI.
- `cmd/emit-content-bundle` — emits a per-language JSON content bundle from a data directory for a downstream consumer.

## Development

Built with Go 1.25 (deps: `gopkg.in/yaml.v3`, `github.com/yuin/goldmark` + `goldmark-meta`, `golang.org/x/image`). The Makefile drives builds, tests, and gates:

```bash
make install              # go install ./cmd/website-compiler
make build  WEBSITE_ROOT=../my-website [DIST_DIR=dist]
make serve  WEBSITE_ROOT=../my-website          # :8080
make fmt-check && make lint   # gofmt check + golangci-lint
make validate                 # go vet + compile check
make test                     # go test -race -shuffle=on ./...
make coverage-gate            # fail under COVERAGE_MIN (default 90)
make smoke-check              # build hello-world via build-static, assert index.html
make quality-gates            # test + race + coverage + govulncheck + smoke
make container-build          # build CLI container from containers/Dockerfile.cli
make help                     # list targets
```

Build variants: `make build-inline` (inlined assets), `make build-no-assets` (skip asset copy). Git hooks (lefthook) install via `make lefthook`; local CI via `make ci-local`.

## License

Proprietary — All Rights Reserved. Copyright (c) 2026 Felipe Fuhr. See [LICENSE](LICENSE).
