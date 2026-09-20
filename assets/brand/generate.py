#!/usr/bin/env python3
"""Sprout brand asset generator (Phase 0.5.1).

Deterministic source of truth for the Sprout logo family. The grids
below are a clean-room reconstruction of the approved Sprout artwork
(docs/BRAND.md §reference) on strict grids — they are the canonical
geometry; every SVG/PNG/ASCII asset is derived from them.

Run:  python3 assets/brand/generate.py
Out:  assets/brand/*.svg, *.png, sprout-blocks.txt

PNG raster exports use integer cell multiples only (no interpolation);
the SVGs are the scalable source of truth. QML never loads these files —
plugins/local.omatorrent/SproutGlyph.qml embeds the same compact grid
(tools/validate_brand.py asserts the two stay in sync).
"""

from pathlib import Path
import subprocess

OUT = Path(__file__).parent

# ---- Palette (extracted from the approved artwork; see BRAND.md) ----
GREEN = "#A1D06A"   # Sprout green — primary brand accent
ORANGE = "#EDC110"  # warm orange — secondary detail accent
LIGHT = "#D9D9DA"   # light foreground (mono assets)
DARK = "#141415"    # dark base (documentation/logo background)

# ---- Glyph, full variant: 24x18 grid (unit u = half letter-module).
# Two asymmetric leaves with inner notches + central stem; the stem
# extends 1u below the letter baseline (rows 0..16, letters are 16u).
GLYPH_FULL = [
    "........................",
    "...............#######..",
    ".#######.....#########..",
    ".########....#########..",
    ".####.####..####..####..",
    ".####.####..####...###..",
    ".###...###..####..####..",
    ".###...###..#########...",
    ".####.####..#########...",
    "..#################.....",
    "...###########..........",
    ".........#####..........",
    "..........###...........",
    "..........##............",
    "..........##............",
    "..........##............",
    "..........##............",
    "........................",
]

# ---- Glyph, compact variant: 12x10 grid — hand-finished 2x2 reduction
# of the full grid (notches dropped: invisible at 16-32 px; the
# two-leaf silhouette and stem position are preserved).
# Target: bar widget / panel header / 16-32 px.
GLYPH_COMPACT = [
    "............",
    "......####..",
    ".####.#####.",
    ".####.#####.",
    ".##########.",
    "..########..",
    "....#####...",
    ".....##.....",
    ".....##.....",
    ".....##.....",
]

# ---- Wordmark letters: 8 module rows tall, strokes 2 cells, counters
# 1 cell, top bars cut 1 cell in from the left (O/T symmetric).
# Faithful to the approved artwork's letter construction.
LETTERS = {
    "S": [".####",
          "##.##",
          "##.##",
          "##...",
          "#####",
          "##.##",
          "##.##",
          ".####"],
    "P": [".####",
          "##.##",
          "##.##",
          "#####",
          "##...",
          "##...",
          "##...",
          "##..."],
    "R": [".####",
          "##.##",
          "##.##",
          "#####",
          "####.",
          "#####",
          "##.##",
          "##..#"],
    "O": [".####.",
          "##..##",
          "##..##",
          "##..##",
          "##..##",
          "##..##",
          "##..##",
          ".####."],
    "U": ["##.##",
          "##.##",
          "##.##",
          "##.##",
          "##.##",
          "##.##",
          "##.##",
          ".###."],
    "T": ["#####",
          ".###.",
          ".###.",
          ".###.",
          ".###.",
          ".###.",
          ".###.",
          ".###."],
}
WORD = "SPROUT"
LETTER_GAP_U = 1  # half-module between letters (tight, matches the artwork)


def wordmark_grid():
    """Wordmark rows in half-module units (each letter module = 2u).
    Returns (rows_u, width_u)."""
    exp_letters = []
    for ch in WORD:
        for row in LETTERS[ch]:
            exp_letters.append("".join(c * 2 for c in row))
    # rows_u[r] = letter row r (module) duplicated into 2 u-rows
    rows_u = []
    for r in range(8):
        line = ""
        for i, ch in enumerate(WORD):
            line += "".join(c * 2 for c in LETTERS[ch][r])
            if i < len(WORD) - 1:
                line += "." * LETTER_GAP_U
        rows_u.append(line)
        rows_u.append(line)
    width_u = sum(len(LETTERS[ch][0]) for ch in WORD) * 2 \
        + (len(WORD) - 1) * LETTER_GAP_U
    return rows_u, width_u


def svg_rects(grid, unit, color, ox=0, oy=0):
    """Emit <rect> run-length compressed per row (deterministic)."""
    parts = []
    for y, row in enumerate(grid):
        x = 0
        while x < len(row):
            if row[x] == "#":
                x0 = x
                while x < len(row) and row[x] == "#":
                    x += 1
                parts.append(
                    f'<rect x="{ox + x0 * unit}" y="{oy + y * unit}" '
                    f'width="{(x - x0) * unit}" height="{unit}" fill="{color}"/>')
            else:
                x += 1
    return parts


def write_svg(name, w, h, rects, comment):
    body = "\n".join("  " + r for r in rects)
    svg = (f'<?xml version="1.0" encoding="UTF-8"?>\n'
           f'<!-- Sprout brand asset: {comment}. Deterministic output of '
           f'assets/brand/generate.py — edit the grids there, not this file. -->\n'
           f'<svg xmlns="http://www.w3.org/2000/svg" width="{w}" height="{h}" '
           f'viewBox="0 0 {w} {h}" shape-rendering="crispEdges">\n'
           f'{body}\n</svg>\n')
    (OUT / name).write_text(svg)
    print("wrote", name)


def export_png(svg_name, png_name, out_w, out_h, bg=None):
    # -background must be a READ-time option (before the input) for the
    # SVG rasterizer to keep/replace alpha; deterministic output: no
    # date/time chunks (tools/validate_brand.py regenerate-diff).
    cmd = ["magick", "-background", bg if bg else "none",
           str(OUT / svg_name), "-filter", "point",
           "-resize", f"{out_w}x{out_h}!",
           "-define", "png:exclude-chunk=date,time"]
    if bg:
        cmd += ["-alpha", "remove", "-alpha", "off"]
    cmd.append(str(OUT / png_name))
    subprocess.run(cmd, check=True)
    print("wrote", png_name)


def main():
    U = 8  # SVG unit for the full-size sources (px per grid cell)

    # --- Glyph SVGs (grid cell = 1u) ---
    write_svg("sprout-glyph.svg", 24 * U, 18 * U,
              svg_rects(GLYPH_FULL, U, GREEN),
              "compact glyph is the small-size variant; this is the full sprout glyph (24x18 grid)")
    write_svg("sprout-glyph-mono.svg", 24 * U, 18 * U,
              svg_rects(GLYPH_FULL, U, LIGHT),
              "full sprout glyph, monochrome light foreground")
    write_svg("sprout-glyph-compact.svg", 12 * U, 10 * U,
              svg_rects(GLYPH_COMPACT, U, GREEN),
              "compact sprout glyph (12x10 grid) for 16-32 px surfaces")
    write_svg("sprout-glyph-compact-mono.svg", 12 * U, 10 * U,
              svg_rects(GLYPH_COMPACT, U, LIGHT),
              "compact sprout glyph, monochrome light foreground")

    # --- Wordmark (letters only, half-module grid) ---
    wrows, wu = wordmark_grid()
    wpx = wu * U
    write_svg("sprout-wordmark.svg", wpx, 16 * U,
              svg_rects(wrows, U, GREEN),
              "SPROUT wordmark, letters only")
    write_svg("sprout-wordmark-mono.svg", wpx, 16 * U,
              svg_rects(wrows, U, LIGHT),
              "SPROUT wordmark, monochrome light foreground")

    # --- Lockup: glyph + 3u gap + wordmark, shared cap line ---
    gap = 3
    lock_w = 24 + gap + wu
    rects = svg_rects(GLYPH_FULL, U, GREEN)
    rects += svg_rects(wrows, U, GREEN, ox=(24 + gap) * U)
    write_svg("sprout-lockup.svg", lock_w * U, 18 * U, rects,
              "primary lockup: sprout glyph + SPROUT wordmark")

    # --- Terminal / block variant (same geometry, half-block packing) ---
    lines = []
    for gy in range(0, 18, 2):
        line = ""
        for gx in range(lock_w):
            gtop = GLYPH_FULL[gy][gx] if gx < 24 and gy < len(GLYPH_FULL) else "."
            gbot = (GLYPH_FULL[gy + 1][gx]
                    if gx < 24 and gy + 1 < len(GLYPH_FULL) else ".")
            if gx >= 24 + gap:
                lx = gx - 24 - gap
                # letters occupy rows 0..15; glyph rows 0..16
                ltop = wrows[gy][lx] if gy < len(wrows) and lx < len(wrows[0]) else "."
                lbot = wrows[gy + 1][lx] if gy + 1 < len(wrows) and lx < len(wrows[0]) else "."
            else:
                ltop = lbot = "."
            top = gtop == "#" or ltop == "#"
            bot = gbot == "#" or lbot == "#"
            line += "\u2580" if top and not bot else (
                "\u2584" if bot and not top else (
                    "\u2588" if top and bot else " "))
        lines.append(line.rstrip())
    term = "\n".join(lines).rstrip() + "\n"
    (OUT / "sprout-blocks.txt").write_text(term)
    print("wrote sprout-blocks.txt")

    # --- PNG raster exports (integer multiples only) ---
    # Transparent variants (true alpha): small-size glyph exports.
    export_png("sprout-glyph-compact.svg", "sprout-glyph-12.png", 12, 10)
    export_png("sprout-glyph-compact.svg", "sprout-glyph-24.png", 24, 20)
    export_png("sprout-glyph-compact.svg", "sprout-glyph-36.png", 36, 30)
    export_png("sprout-glyph-compact.svg", "sprout-glyph-48.png", 48, 40)
    export_png("sprout-glyph-mono.svg", "sprout-glyph-mono-64.png", 64, 48)
    export_png("sprout-lockup.svg", "sprout-lockup-transparent.png", 752, 144)
    # Dark-base variants (documentation surfaces): flattened on #141415.
    export_png("sprout-glyph.svg", "sprout-glyph-dark-64.png", 64, 48, bg=DARK)
    export_png("sprout-glyph.svg", "sprout-glyph-dark-128.png", 128, 96, bg=DARK)
    export_png("sprout-lockup.svg", "sprout-lockup-dark.png", 752, 144, bg=DARK)


if __name__ == "__main__":
    main()
