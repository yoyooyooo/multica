# Offline `next/font/google` mock

`Dockerfile.web` and `@multica/web` production builds must not fetch Google Fonts at compile time. Next 16 reads `NEXT_FONT_GOOGLE_MOCKED_RESPONSES` and, for `src: url(/absolute/path)`, loads the WOFF2 from disk.

This directory is that mock. It covers the four families used by `apps/web`:

- Inter
- Geist Mono
- Source Serif 4
- Instrument Serif

The files are SIL OFL 1.1 faces, subsetted the same way Google Fonts served them for the Mini r2 frontend image.

## Default path

- `Dockerfile.web` sets `NEXT_FONT_GOOGLE_MOCKED_RESPONSES` to this `responses.cjs`
- `pnpm --filter @multica/web build` goes through `with-mocked-google-fonts.cjs`

Do not add a new `next/font/google` call without updating `font-requests.json` and regenerating the mock. The vitest file in this directory fails closed on missing URLs or missing files.

## Regenerate

Run this only on a host that can reach `fonts.googleapis.com` and `fonts.gstatic.com`, after changing the `next/font/google` calls:

```bash
node apps/web/offline-fonts/generate.cjs
```

Then run `pnpm --filter @multica/web test -- offline-fonts/responses.test.ts`.
