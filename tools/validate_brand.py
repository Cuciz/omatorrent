#!/usr/bin/env python3
"""Brand validation gate (Phase 0.5.1, docs/BRAND.md).

Checks:
 1. Grid sync: the QML SproutGlyph grids (both plugin copies) are
    byte-identical to the canonical grids in assets/brand/generate.py.
 2. Asset inventory: every generated SVG/PNG/TXT exists, PNGs have the
    documented integer-multiple dimensions, transparent variants have
    real alpha, dark variants use the brand dark base.
 3. Public-name grep: no unclassified old public name ("OmaTorrent")
    in user-facing surfaces (manifests, QML display strings, READMEs).
 4. Deterministic regeneration: running generate.py yields no diff.

Exit 0 = PASS. Any failure prints FAIL lines and exits 1.
"""

import json
import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
BRAND = ROOT / "assets" / "brand"
PLUGINS = [ROOT / "plugins" / "local.omatorrent",
           ROOT / "plugins" / "local.omatorrent-dashboard"]

fails = []


def check(name, ok, detail=""):
    print(("PASS" if ok else "FAIL"), name, detail)
    if not ok:
        fails.append(name)


# ---- 1. Grid sync ----------------------------------------------------
sys.path.insert(0, str(BRAND))
import generate  # noqa: E402

qml_grids = {}
for plugin in PLUGINS:
    src = (plugin / "SproutGlyph.qml").read_text()
    grids = {}
    for m in re.finditer(r'"(compact|full|wordmark)": \[(.*?)\]', src, re.S):
        rows = re.findall(r'"([.#]+)"', m.group(2))
        grids[m.group(1)] = rows
    qml_grids[plugin.name] = grids

wordmark_rows, _ = generate.wordmark_grid()
canonical = {"compact": generate.GLYPH_COMPACT,
             "full": generate.GLYPH_FULL,
             "wordmark": wordmark_rows}
for plugin, grids in qml_grids.items():
    for key, rows in canonical.items():
        check(f"grid sync {plugin}:{key}", grids.get(key) == rows,
              f"qml={len(grids.get(key, []))}r canonical={len(rows)}r")

a = (PLUGINS[0] / "SproutGlyph.qml").read_bytes()
b = (PLUGINS[1] / "SproutGlyph.qml").read_bytes()
check("SproutGlyph.qml copies identical", a == b)

# ---- 2. Asset inventory ----------------------------------------------
PNG_DIMS = {  # name: (width, height, kind) kind: "t"=transparent, "d"=dark base
    "sprout-glyph-12.png": (12, 10, "t"),
    "sprout-glyph-24.png": (24, 20, "t"),
    "sprout-glyph-36.png": (36, 30, "t"),
    "sprout-glyph-48.png": (48, 40, "t"),
    "sprout-glyph-mono-64.png": (64, 48, "t"),
    "sprout-lockup-transparent.png": (752, 144, "t"),
    "sprout-glyph-dark-64.png": (64, 48, "d"),
    "sprout-glyph-dark-128.png": (128, 96, "d"),
    "sprout-lockup-dark.png": (752, 144, "d"),
}
for name, (w, h, kind) in PNG_DIMS.items():
    f = BRAND / name
    if not f.exists():
        check(f"asset {name}", False, "missing")
        continue
    out = subprocess.run(["magick", "identify", "-format", "%w %h",
                          str(f)], capture_output=True, text=True)
    got = tuple(int(x) for x in out.stdout.split()) if out.stdout else ()
    check(f"asset {name} {w}x{h}", got == (w, h),
          f"got {got[0]}x{got[1]}" if got else "identify failed")
    if got != (w, h):
        continue
    # %[fx:mean] on the alpha channel: 0..1 (some ink but not full)
    out = subprocess.run(["magick", str(f), "-alpha", "extract",
                          "-format", "%[fx:mean]", "info:"],
                         capture_output=True, text=True).stdout
    try:
        mean = float(out)
    except ValueError:
        mean = -1
    corner = subprocess.run(["magick", str(f), "-format",
                             "%[pixel:p{0,0}]", "info:"],
                            capture_output=True, text=True).stdout.strip()
    if kind == "t":
        check(f"alpha {name}", 0.05 < mean < 0.95, f"alpha mean={mean:.2f}")
    else:
        check(f"base {name}", "20,20,21" in corner, corner)
    hist = subprocess.run(["magick", str(f), "-format", "%c",
                           "-define", "histogram:unique-colors=true",
                           "histogram:info:"], capture_output=True, text=True).stdout
    if "mono" in name:
        check(f"ink {name}", "217,217,218" in hist, "no light ink found")
    else:
        check(f"ink {name}", "161,208,106" in hist, "no green ink found")

for name in ["sprout-glyph.svg", "sprout-glyph-compact.svg",
             "sprout-wordmark.svg", "sprout-lockup.svg",
             "sprout-glyph-mono.svg", "sprout-glyph-compact-mono.svg",
             "sprout-wordmark-mono.svg", "sprout-blocks.txt"]:
    check(f"asset {name}", (BRAND / name).exists())

# alpha: transparent raster exports only exist as dark-base variants in
# the inventory; the SVGs must carry no fill but the brand colors.
svg_src = "\n".join((BRAND / n).read_text() for n in
                    ["sprout-glyph.svg", "sprout-glyph-compact.svg",
                     "sprout-wordmark.svg", "sprout-lockup.svg"])
check("SVG brand colors only", re.findall(r'fill="([^"]+)"', svg_src) == ["#A1D06A"] * svg_src.count("fill="))
mono_src = "\n".join((BRAND / n).read_text() for n in
                     ["sprout-glyph-mono.svg", "sprout-glyph-compact-mono.svg",
                      "sprout-wordmark-mono.svg"])
check("SVG mono colors only", re.findall(r'fill="([^"]+)"', mono_src) == ["#D9D9DA"] * mono_src.count("fill="))

# ---- 3. Public-name grep ----------------------------------------------
ALLOW_OLD = re.compile(
    r"formerly|working name|internals|→ Sprout|OmaTorrent Phase 0"
    r"|replacing the user-facing|RENAME|MIGRATE|KEEP|HISTORY|audit", re.I)
surfaces = [ROOT / "README.md"]
for plugin in PLUGINS:
    surfaces += [plugin / "manifest.json", plugin / "README.md"]

for f in surfaces:
    for i, line in enumerate(f.read_text().splitlines(), 1):
        if "OmaTorrent" in line and not ALLOW_OLD.search(line):
            check(f"public-name {f.name}:{i}", False, line.strip())
            break
    else:
        check(f"public-name {f.name}", True)

# QML display strings: the old name must not appear in any Text text
for plugin in PLUGINS:
    for qml in plugin.glob("*.qml"):
        for i, line in enumerate(qml.read_text().splitlines(), 1):
            if re.search(r'text:.*OmaTorrent', line):
                check(f"qml string {qml.name}:{i}", False, line.strip())
check("qml strings clean", not any(f.startswith("qml string") for f in fails))

# ---- 4. Deterministic regeneration -------------------------------------
before = {p.name: p.read_bytes() for p in BRAND.iterdir() if p.is_file()}
subprocess.run([sys.executable, str(BRAND / "generate.py")],
               capture_output=True, check=True)
after = {p.name: p.read_bytes() for p in BRAND.iterdir() if p.is_file()}
diff = [k for k in before if before[k] != after.get(k)]
check("assets regenerate deterministically", before == after,
      f"diff: {diff}" if diff else "")

print()
if fails:
    print(f"BRAND VALIDATION: FAIL ({len(fails)} failures)")
    sys.exit(1)
print("BRAND VALIDATION: PASS")
