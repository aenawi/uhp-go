# Brand assets

| File | Use |
|---|---|
| [`banner.png`](banner.png) | The README header. 2560×640, so it stays sharp on a retina screen. |
| [`banner.svg`](banner.svg) | Source for the banner. Edit this, then re-render the PNG. |
| [`logo.svg`](logo.svg) | The mark on its own, no text. Square, legible down to about 24px. |
| `logo-256.png` · `logo-512.png` · `logo-1024.png` | The same mark rasterised, transparent background. |

Re-render the PNGs after any edit to the SVGs:

```sh
rsvg-convert -w 2560 -h 640 docs/assets/banner.svg -o docs/assets/banner.png
for s in 256 512 1024; do
  rsvg-convert -w $s -h $s docs/assets/logo.svg -o docs/assets/logo-$s.png
done
```

## What it's meant to say

A gopher standing on one bus, with harnesses hanging off it. That's the product in a picture:
one HTTP contract, and however many agent harnesses you have underneath it.

The bus deliberately runs off both edges of the banner and its nodes fade out as they go.
There is no last harness, and the mark shouldn't imply one — the count is five today and the
picture shouldn't need redrawing when it isn't.

Colours are Go's own — `#00ADD8` and `#007D9C` — with `#16232A` for the eyes and snout. The
mark draws no background, so it sits on light and dark equally. The banner carries its own
dark ground, which is why it reads the same in either GitHub theme.

## Two things worth knowing before you reuse it

**The gopher here is original geometry, not the Go Gopher.** The Go Gopher was drawn by Renée
French and is her work; this is a gopher-shaped mark built from circles and rounded rectangles,
drawn for this repository. If you ever want to use *her* Gopher instead, that needs attribution
under its own licence, and this file should be updated to say so.

**The Go name and logo are trademarks of Google.** This mark deliberately uses neither.
Borrowing Go's palette is not the same as claiming Go's brand, but if that ever gets
uncomfortable, the palette is the part to change.

The banner sets the name in whatever monospace font the renderer has. That's fine for a PNG
built on a machine that has Menlo; if you need it identical everywhere, convert the text to
outlines before rendering.
