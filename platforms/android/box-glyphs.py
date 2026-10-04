#!/usr/bin/env python3
# Run once on res/font/ubuntu_sans_mono.ttf after subsetting it: python3 box-glyphs.py app/src/main/res/font/ubuntu_sans_mono.ttf
"""Draws the box-drawing and shape glyphs Mermaid art uses that Ubuntu Sans Mono lacks, in its own
cell (560 wide, light strokes 90 on the 280/370 axes, cells from -190 to 930), so diagrams align."""
import math, sys
from fontTools.ttLib import TTFont
from fontTools.pens.ttGlyphPen import TTGlyphPen

path = sys.argv[1]
f = TTFont(path)
for tag in ('glyf', 'gvar', 'hmtx', 'HVAR', 'cmap'):
    f[tag]  # decode before the glyph order grows
W, X, Y, B, T = 560, 280, 370, -190, 930
L, H = 45, 80  # half widths: light, heavy

def rect(p, x0, y0, x1, y1):  # clockwise
    p.moveTo((x0, y0)); p.lineTo((x0, y1)); p.lineTo((x1, y1)); p.lineTo((x1, y0)); p.closePath()

def poly(p, pts, hole=False):
    pts = pts[::-1] if hole else pts
    p.moveTo(pts[0]); [p.lineTo(q) for q in pts[1:]]; p.closePath()

def arms(p, dirs, h):
    if 'l' in dirs: rect(p, 0, Y - h, X + h, Y + h)
    if 'r' in dirs: rect(p, X - h, Y - h, W, Y + h)
    if 'u' in dirs: rect(p, X - h, Y - h, X + h, T)
    if 'd' in dirs: rect(p, X - h, B, X + h, Y + h)

def arc(p, cx, cy, a0):  # quarter ring around a cell corner, from angle a0 over 90 degrees
    rx, ry = X, Y - B
    outer = [(cx + (rx + L) * math.cos(a), cy + (ry + L) * math.sin(a)) for a in [math.radians(a0 + 90 * i / 16) for i in range(17)]]
    inner = [(cx + (rx - L) * math.cos(a), cy + (ry - L) * math.sin(a)) for a in [math.radians(a0 + 90 * i / 16) for i in range(17)]]
    pts = [(round(x), round(y)) for x, y in outer + inner[::-1]]
    poly(p, pts if a0 in (90, 270) else pts[::-1])

def shape(p, pts, hollow):
    poly(p, pts)
    if hollow:
        cx, cy = sum(x for x, _ in pts) / len(pts), sum(y for _, y in pts) / len(pts)
        poly(p, [(round(cx + (x - cx) * 0.6), round(cy + (y - cy) * 0.6)) for x, y in pts], hole=True)

right, left = [(110, 170), (110, 570), (470, 370)], [(450, 170), (90, 370), (450, 570)]
up, down = [(80, 190), (280, 560), (480, 190)], [(80, 550), (480, 550), (280, 180)]
diamond = [(280, 150), (70, 370), (280, 590), (490, 370)]
circle = [(round(X + 190 * math.cos(math.radians(a))), round(Y + 190 * math.sin(math.radians(a)))) for a in range(360, 0, -15)]

glyphs = {
    0x2501: lambda p: arms(p, 'lr', H), 0x2503: lambda p: arms(p, 'ud', H),
    0x250f: lambda p: arms(p, 'dr', H), 0x2513: lambda p: arms(p, 'dl', H), 0x2517: lambda p: arms(p, 'ur', H), 0x251b: lambda p: arms(p, 'ul', H),
    0x2523: lambda p: arms(p, 'udr', H), 0x252b: lambda p: arms(p, 'udl', H), 0x2533: lambda p: arms(p, 'lrd', H), 0x253b: lambda p: arms(p, 'lru', H),
    0x254b: lambda p: arms(p, 'lrud', H),
    0x254c: lambda p: (rect(p, 30, Y - L, 250, Y + L), rect(p, 310, Y - L, 530, Y + L)),
    0x254e: lambda p: (rect(p, X - L, B + 40, X + L, 330), rect(p, X - L, 410, X + L, T - 40)),
    0x256d: lambda p: arc(p, W, B, 90), 0x256e: lambda p: arc(p, 0, B, 0), 0x256f: lambda p: arc(p, 0, T, 270), 0x2570: lambda p: arc(p, W, T, 180),
    0x25b2: lambda p: shape(p, up, False), 0x25b3: lambda p: shape(p, up, True), 0x25b6: lambda p: shape(p, right, False), 0x25b7: lambda p: shape(p, right, True),
    0x25bc: lambda p: shape(p, down, False), 0x25bd: lambda p: shape(p, down, True), 0x25c1: lambda p: shape(p, left, True), 0x25c4: lambda p: shape(p, left, False),
    0x25c0: lambda p: shape(p, left, False),
    0x25c6: lambda p: shape(p, diamond, False), 0x25c7: lambda p: shape(p, diamond, True), 0x25cf: lambda p: shape(p, circle, False),
}
cmap = f.getBestCmap()
order = list(f.getGlyphOrder())
for code, draw in glyphs.items():
    if code in cmap:
        continue
    name = "uni%04X" % code
    pen = TTGlyphPen(None)
    draw(pen)
    f['glyf'][name] = pen.glyph()
    order.append(name)
    f['hmtx'][name] = (W, 0)
    f['gvar'].variations[name] = []
    for t in f['cmap'].tables:
        if t.isUnicode():
            t.cmap[code] = name
f.setGlyphOrder(order)
f['glyf'].glyphOrder = order
f['maxp'].numGlyphs = len(order)
f.save(path)
print("added", len(glyphs), "glyphs")
