# Self-hosted font inventory

All fonts are served from `/fonts/` and declared via `@font-face` in
`apps/web/src/tokens.css`. No external CDN at runtime.

## Recommended CSP (production)

```
default-src 'self'; font-src 'self'; style-src 'self' 'unsafe-inline';
```

Apply at the gateway/reverse-proxy layer (Traefik, nginx, Caddy).
Do not add as a `<meta>` tag — it would block Vite's HMR in development.

---

## Inter (variable font, Latin subsets)

| Field   | Value                                                           |
| ------- | --------------------------------------------------------------- |
| Family  | Inter                                                           |
| Version | v20 (Google Fonts distribution, 2024)                           |
| Weights | 100–900 (variable axis)                                         |
| License | SIL Open Font License 1.1 — https://scripts.sil.org/OFL         |
| Source  | https://rsms.me/inter / https://fonts.google.com/specimen/Inter |

| File                    | Subset                         | SHA-256                                                            |
| ----------------------- | ------------------------------ | ------------------------------------------------------------------ |
| `inter-latin.woff2`     | Latin (U+0000–00FF + common)   | `3100e775e8616cd2611beecfa23a4263d7037586789b43f035236a2e6fbd4c62` |
| `inter-latin-ext.woff2` | Latin Extended (U+0100–02FF +) | `34b9c504cab7a73e37b746343a449132e56cf7b5481af2cb81dc74dcff25c956` |

Downloaded from:

- `https://fonts.gstatic.com/s/inter/v20/UcC73FwrK3iLTeHuS_nVMrMxCp50SjIa1ZL7.woff2`
- `https://fonts.gstatic.com/s/inter/v20/UcC73FwrK3iLTeHuS_nVMrMxCp50SjIa25L7SUc.woff2`

---

## Material Symbols Outlined (icon ligature font)

| Field   | Value                                                                            |
| ------- | -------------------------------------------------------------------------------- |
| Family  | Material Symbols Outlined                                                        |
| Version | v354 (Google Fonts distribution, 2024)                                           |
| Style   | Variable font (opsz 20–48, wght 100–700, FILL 0–1, GRAD -50–200)                 |
| License | Apache License 2.0 — https://www.apache.org/licenses/LICENSE-2.0                 |
| Source  | https://fonts.google.com/icons / https://github.com/google/material-design-icons |

| File                              | SHA-256                                                            |
| --------------------------------- | ------------------------------------------------------------------ |
| `material-symbols-outlined.woff2` | `577f0c935508c44c508d83eb17d2cb10b907bd42bff0331bccaff2df3f912340` |

Downloaded from:

```
https://fonts.gstatic.com/s/materialsymbolsoutlined/v354/kJF1BvYX7BgnkSrUwT8OhrdQw4oELdPIeeII9v6oDMzByHX9rA6RzaxHMPdY43zj-jCxv3fzvRNU22ZXGJpEpjC_1v-p_4MrImHCIJIZrDCvHOej.woff2
```

---

## Verification

```bash
sha256sum apps/web/public/fonts/*.woff2
```
