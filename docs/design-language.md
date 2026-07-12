# Evergreen — the NextLeaf design language

Evergreen is NextLeaf's visual language: **cozy but current**. Warm and book-ish in
feeling, built like modern software. It grew out of the original dark forest-green
selector page and keeps that heritage — the leaf, the green, the serif — while
tightening everything around one quiet, book-ish gesture.

The reference implementation is `internal/web/select.html`; every token below is
defined in its `:root` block.

## Principles

1. **One gesture, everything else disciplined.** The bookmark ribbon draped over the
   recommendation card is the single decorative element. New screens don't add
   ornaments; they earn character through type, spacing, and the palette.
2. **Serif where the book speaks.** Book titles and the wordmark are set in serif.
   UI text — labels, buttons, metadata, notices — is system sans. Never the reverse.
3. **Light and dark are a designed pair.** Both modes are first-class and defined
   together, not one inverted from the other. Anything added must be specified in
   both.
4. **Variety is the voice.** Copy explains *why* a pick breaks or extends the
   reader's pattern ("In favour" / "Trade-offs"), in plain active sentences, from
   the reader's side of the screen.
5. **Self-contained.** No external fonts, scripts, or styles. System font stacks
   only; everything ships in the template. The app must render fully offline.

## Color tokens

Tokens are expressed with CSS `light-dark()`, so each has a light and dark value and
resolves automatically with the active `color-scheme`.

| Token          | Light                  | Dark                     | Role |
| -------------- | ---------------------- | ------------------------ | ---- |
| `--bg`         | `#eef0e6`              | `#171d18`                | Page ground — warm green-biased neutral |
| `--card`       | `#fbfaf5`              | `#202a21`                | Raised surfaces: cards, notices |
| `--ink`        | `#24312a`              | `#e9ece2`                | Primary text |
| `--muted`      | `#64756a`              | `#9aa894`                | Secondary text: authors, metadata, trade-offs |
| `--moss`       | `#3f7d54`              | `#7cc491`                | The accent: leaf mark, ribbon, buttons, eyebrows, "in favour" markers |
| `--on-moss`    | `#fbfaf5`              | `#171d18`                | Text/icons on a moss fill |
| `--tint`       | `rgba(63,125,84,.10)`  | `rgba(124,196,145,.12)`  | Soft moss wash: tag pills, hollow markers, code |
| `--line`       | `rgba(36,49,42,.14)`   | `rgba(233,236,226,.12)`  | Hairline borders |
| `--error`      | `#8c3b34`              | `#e39e96`                | Error text and borders |
| `--error-tint` | `rgba(140,59,52,.07)`  | `rgba(227,158,150,.09)`  | Error surface wash |
| `--shadow-c`   | `rgba(36,49,42,.35)`   | `rgba(0,0,0,.6)`         | Shadow color (used in `0 12px 28px -16px`) |

Rules of thumb:

- **Moss is the only accent.** Semantic error red exists for failures and doesn't
  count as an accent. Don't introduce new hues; derive washes from `--tint`.
- Neutrals are green-biased on purpose — never substitute pure grays.
- Positive/negative pairs (pros vs. cons) are *filled moss* vs. *tinted neutral*,
  not green vs. red. Red is reserved for actual failures.

## Typography

| Role | Stack | Usage |
| ---- | ----- | ----- |
| Serif (display) | `Charter, "Bitstream Charter", "Iowan Old Style", "Palatino Linotype", Georgia, serif` | Wordmark, book titles, blank-cover fallback |
| Sans (UI) | `system-ui, -apple-system, "Segoe UI", Roboto, Helvetica, Arial, sans-serif` | Everything else |

Scale and treatments in use:

- Wordmark: serif 600, `1.7rem`, `-0.01em` tracking.
- Book title: serif 600, `1.7rem`, line-height `1.1`.
- Body/reasons: sans, `0.92rem`.
- Metadata: sans, `0.8–0.95rem`, always `--muted`.
- Eyebrows/labels ("Recommended", "In favour"): sans 600, `0.68rem`, uppercase,
  `0.14em` letter-spacing. Moss for positive/primary labels, muted for secondary.

## Shape, depth, and spacing

- Radii: `16px` for cards, `12px` for notices, `6px` for covers, `999px` (pill) for
  buttons and tags. Nothing square, nothing fully round except pills and dots.
- One shadow recipe: `0 12px 28px -16px var(--shadow-c)` for cards and notices,
  `0 8px 18px -10px` for covers. Shadows are soft and warm, never hard.
- The page is a single centered column, `max-width: 680px`. Section rhythm is
  `~1.6rem` between blocks; let flex/grid `gap` do the spacing inside groups.

## Components

- **Masthead** — leaf mark (moss) + serif wordmark, with the theme toggle pushed to
  the far right as a quiet outlined circle.
- **Recommendation card** — raised `--card` surface holding cover and details, with
  the **bookmark ribbon** hanging over its top-right edge (moss, notched tail via
  `clip-path`). The ribbon appears only on the recommendation card — it marks "your
  next read", so it never repeats elsewhere on a screen.
- **Cover** — `2:3`, `6px` radius, soft shadow. Missing covers fall back to a
  `--tint` block with the title in serif, never a gray box.
- **Reason lists** — each reason is a row with a 17px circular marker: filled moss
  `+` for "In favour", tinted `–` for "Trade-offs". Trade-off text is muted; it's a
  caveat, not a warning.
- **Tag pills** — `--tint` background, muted text, pill radius. Capped at four.
- **Buttons** — moss pill, `--on-moss` text, 600 weight. Hover lifts 1px and
  brightens slightly. Secondary actions are plain underlined muted links.
- **Notices** — card-styled panels for neutral states (unconfigured, empty list);
  the error variant swaps to `--error` border/text on `--error-tint`, flat (no
  shadow). Notices explain what happened and what to do next.

## Light/dark mechanics

- `:root` declares `color-scheme: light dark` and all tokens via `light-dark()`.
- Default follows the OS (`prefers-color-scheme`).
- The masthead toggle overrides it by setting `data-theme="light|dark"` on `<html>`
  (which pins `color-scheme`), persisted in `localStorage("theme")`. Toggling back
  to the browser's own preference clears the override, so the page resumes
  following the OS. A tiny inline script in `<head>` re-applies any stored
  override before first paint to avoid flashing.
- New styles must use tokens only — no raw hex in component rules — so both modes
  stay correct for free.

## Voice and copy

- Buttons say exactly what they do: "Pick another", not "Submit" or "Reroll".
- Reasons are complete, plain sentences naming the pattern they break or extend:
  "A standalone, after a long series run."
- Section labels are fixed vocabulary: **Recommended**, **In favour**,
  **Trade-offs**. Tests in `internal/web/web_test.go` pin these — changing the
  vocabulary is a product decision, not a styling one.
- Empty and error states give a next step ("add a few books and come back"), never
  a bare apology.

## Accessibility floor

- Keyboard focus is always visible: `2px` moss outline, `3px` offset.
- `prefers-reduced-motion` disables the hover lift and toggle transitions; motion
  is limited to sub-200ms micro-interactions regardless.
- Muted-on-card and moss-on-card pairings meet WCAG AA at their used sizes in both
  modes; check any new pairing in both modes before shipping.
- Decorative elements (ribbon, marker glyphs) carry `aria-hidden="true"`.
