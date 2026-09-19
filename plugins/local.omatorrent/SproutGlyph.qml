// Sprout brand glyph (Phase 0.5.1, docs/BRAND.md): renders the Sprout
// logo grids deterministically from the theme foreground — no raster
// assets, no font. Grids are the canonical geometry from
// assets/brand/generate.py (tools/validate_brand.py asserts sync).
// Presentation only (ADR-0001); purely decorative, no input handling.
import QtQuick
import qs.Commons

Canvas {
  id: root

  // "compact" (12x10) for bar/header surfaces 16-32 px, "full" (24x18)
  // for empty state / larger brand contexts, "wordmark" for the SPROUT
  // pixel wordmark (empty state title only — docs/BRAND.md §usage).
  property string variant: "compact"
  property color glyphColor: Style.foreground

  readonly property var grids: ({
    "compact": [
      "............",
      "......####..",
      ".####.#####.",
      ".####.#####.",
      ".##########.",
      "..########..",
      "....#####...",
      ".....##.....",
      ".....##.....",
      ".....##....."
    ],
    "full": [
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
      "........................"
    ],
    "wordmark": [
      "..########...########...########...########...####..####.##########",
      "..########...########...########...########...####..####.##########",
      "####..####.####..####.####..####.####....####.####..####...######..",
      "####..####.####..####.####..####.####....####.####..####...######..",
      "####..####.####..####.####..####.####....####.####..####...######..",
      "####..####.####..####.####..####.####....####.####..####...######..",
      "####.......##########.##########.####....####.####..####...######..",
      "####.......##########.##########.####....####.####..####...######..",
      "##########.####.......########...####....####.####..####...######..",
      "##########.####.......########...####....####.####..####...######..",
      "####..####.####.......##########.####....####.####..####...######..",
      "####..####.####.......##########.####....####.####..####...######..",
      "####..####.####.......####..####.####....####.####..####...######..",
      "####..####.####.......####..####.####....####.####..####...######..",
      "..########.####.......####....##...########.....######.....######..",
      "..########.####.......####....##...########.....######.....######.."
    ]
  })

  readonly property var grid: grids[variant] !== undefined ? grids[variant] : grids["compact"]
  readonly property int gridCols: grid[0].length
  readonly property int gridRows: grid.length

  // Natural aspect from the grid; size via height (width follows).
  implicitWidth: Math.round(height * gridCols / gridRows)
  onVariantChanged: requestPaint()
  onGlyphColorChanged: requestPaint()

  antialiasing: false

  onPaint: {
    var ctx = getContext("2d")
    ctx.reset()
    // Integer-snapped run-length rects: crisp pixel edges at 1x/2x
    // scales, hard (never blurred) edges at fractional scales.
    ctx.fillStyle = glyphColor
    var cw = width / gridCols
    var ch = height / gridRows
    for (var y = 0; y < gridRows; y++) {
      var run = 0
      for (var x = 0; x <= gridCols; x++) {
        if (x < gridCols && grid[y].charAt(x) === "#") {
          run++
          continue
        }
        if (run > 0) {
          var x0 = Math.round((x - run) * cw)
          var x1 = Math.round(x * cw)
          var yy = Math.round(y * ch)
          var y2 = Math.round((y + 1) * ch)
          ctx.fillRect(x0, yy, x1 - x0, y2 - yy)
          run = 0
        }
      }
    }
  }
}
