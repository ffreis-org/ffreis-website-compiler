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

## Keeping this file current

- **If you discover a fact not reflected here:** add it before finishing your task.
- **If something here is wrong or outdated:** correct it in the same commit as the code change.
- **If you rename a file, command, or concept referenced here:** update the reference.
