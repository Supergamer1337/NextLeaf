# Static assets

`icon.svg` is the source of truth for the Nextleaf mark. It is both served as the
tab icon and inlined into the masthead, so edit it here and both follow.

The PNGs exist only because iOS (`apple-touch-icon`) and Android install prompts
will not take an SVG. Regenerate them after changing `icon.svg`:

    rsvg-convert -w 180 -h 180 --page-width=180 --page-height=180 --top=-19 \
      --background-color=#fbfaf5 -o icon-180.png icon.svg
    rsvg-convert -w 192 -h 192 --page-width=192 --page-height=192 --top=-20 \
      --background-color=#fbfaf5 -o icon-192.png icon.svg
    rsvg-convert -w 512 -h 512 --page-width=512 --page-height=512 --top=-53 \
      --background-color=#fbfaf5 -o icon-512.png icon.svg

The `--top` offsets lift the artwork, whose mass sits low in the viewBox, to the
optical centre of the tile. The background is opaque because iOS composites a
transparent icon onto black.

## Vendored scripts

`htmx.min.js` (2.0.10) and `idiomorph-ext.min.js` (0.7.4) are vendored rather than
loaded from a CDN: the app is self-hosted and must render fully offline. Both are
embedded, so there is nothing to install or build. To update, refetch at a pinned
version and bump the numbers here:

    curl -sSfLo htmx.min.js https://unpkg.com/htmx.org@2.0.10/dist/htmx.min.js
    curl -sSfLo idiomorph-ext.min.js https://unpkg.com/idiomorph@0.7.4/dist/idiomorph-ext.min.js

`idiomorph-ext.min.js` bundles idiomorph with the htmx extension that registers
`morph`, so it is the only idiomorph file needed.
