#!/usr/bin/env python3
"""
Generate the application icon for fnos-openvpn-as.
Renders a VPN shield (lock motif) on a deep-blue circuit-board-style background.
"""
import os
from PIL import Image, ImageDraw, ImageFont

BASE = os.path.join(os.path.dirname(__file__), "fnos")


def load_font(size):
    candidates = [
        r"C:\Windows\Fonts\arialbd.ttf",
        r"C:\Windows\Fonts\arial.ttf",
        r"C:\Windows\Fonts\DejaVuSans-Bold.ttf",
    ]
    for c in candidates:
        try:
            return ImageFont.truetype(c, size)
        except Exception:
            continue
    return ImageFont.load_default()


def rounded_rect(size, radius, fill):
    img = Image.new("RGBA", (size, size), (0, 0, 0, 0))
    ImageDraw.Draw(img).rounded_rectangle([0, 0, size - 1, size - 1], radius=radius, fill=fill)
    return img


def radial_gradient(size, center_color, edge_color, radius):
    img = Image.new("RGBA", (size, size), (0, 0, 0, 0))
    cx, cy = size // 2, size // 2
    max_dist = ((cx) ** 2 + (cy) ** 2) ** 0.5
    pixels = img.load()
    for y in range(size):
        for x in range(size):
            d = ((x - cx) ** 2 + (y - cy) ** 2) ** 0.5 / max_dist
            r = int(center_color[0] + (edge_color[0] - center_color[0]) * d)
            g = int(center_color[1] + (edge_color[1] - center_color[1]) * d)
            b = int(center_color[2] + (edge_color[2] - center_color[2]) * d)
            pixels[x, y] = (r, g, b, 255)
    mask = Image.new("L", (size, size), 0)
    ImageDraw.Draw(mask).rounded_rectangle([0, 0, size - 1, size - 1], radius=radius, fill=255)
    img.putalpha(mask)
    return img


def draw_circuit_bg(img):
    size = img.size[0]
    d = ImageDraw.Draw(img)
    trace_color = (255, 255, 255, 18)
    for i in range(4):
        y = int(size * (0.18 + i * 0.21))
        d.line([(int(size * 0.08), y), (int(size * 0.92), y)], fill=trace_color, width=max(1, size // 128))
        x = int(size * (0.18 + i * 0.21))
        d.line([(x, int(size * 0.08)), (x, int(size * 0.92))], fill=trace_color, width=max(1, size // 128))


def _shield_path(size):
    """Return a list of (x,y) points forming a shield polygon."""
    m = int(size * 0.16)
    top_w = size - 2 * m
    cx = size // 2
    # top-left, top-right, right-down to waist, bottom point, left-up to waist
    return [
        (m, m),
        (size - m, m),
        (size - m, int(size * 0.58)),
        (cx, size - m),
        (m, int(size * 0.58)),
    ]


def draw_icon(size):
    radius = int(size * 0.22)
    img = radial_gradient(size, (0x25, 0x5E, 0xC4), (0x0B, 0x21, 0x40), radius)
    draw_circuit_bg(img)
    d = ImageDraw.Draw(img)

    # shield body
    pts = _shield_path(size)
    # shadow
    shadow = [(x + max(1, size // 90), y + max(1, size // 90)) for (x, y) in pts]
    d.polygon(shadow, fill=(0, 0, 0, 90))
    d.polygon(pts, fill=(0xE8, 0xEE, 0xF2, 255), outline=(0x9F, 0xB3, 0xC8, 255))

    # inner shield (slightly smaller, accent fill)
    m = int(size * 0.22)
    inner = [
        (m, m),
        (size - m, m),
        (size - m, int(size * 0.55)),
        (size // 2, size - m - int(size * 0.06)),
        (m, int(size * 0.55)),
    ]
    d.polygon(inner, fill=(0x2F, 0x6F, 0xED, 255))

    # lock motif: body + shackle
    cx = size // 2
    lock_w = int(size * 0.20)
    lock_h = int(size * 0.16)
    lock_x0 = cx - lock_w // 2
    lock_y0 = int(size * 0.40)
    lock_y1 = lock_y0 + lock_h
    d.rounded_rectangle([lock_x0, lock_y0, lock_x0 + lock_w, lock_y1],
                        radius=int(size * 0.03), fill=(0xFF, 0xFF, 0xFF, 255))
    # shackle (arc)
    sh_r = lock_w // 2
    sh_cx = cx
    sh_cy = lock_y0
    d.arc([sh_cx - sh_r, sh_cy - sh_r, sh_cx + sh_r, sh_cy + sh_r],
          start=180, end=360, fill=(0xFF, 0xFF, 0xFF, 255), width=max(2, size // 90))
    # keyhole
    kh_r = max(2, size // 48)
    d.ellipse([cx - kh_r, lock_y0 + int(lock_h * 0.45) - kh_r,
               cx + kh_r, lock_y0 + int(lock_h * 0.45) + kh_r], fill=(0x2F, 0x6F, 0xED, 255))

    # text VPN
    font = load_font(int(size * 0.15))
    txt = "VPN"
    tb = d.textbbox((0, 0), txt, font=font)
    tw, th = tb[2] - tb[0], tb[3] - tb[1]
    tx = (size - tw) / 2 - tb[0]
    ty = int(size * 0.66)
    d.text((tx, ty), txt, font=font, fill=(0xFF, 0xFF, 0xFF, 255))

    return img


def main():
    for sz, name in [(64, "ICON.PNG"), (256, "ICON_256.PNG")]:
        im = draw_icon(sz)
        im.save(f"{BASE}/{name}")
        print("saved", name, im.size)
    draw_icon(64).save(f"{BASE}/ui/images/64.png")
    draw_icon(256).save(f"{BASE}/ui/images/256.png")
    print("saved ui images")


if __name__ == "__main__":
    main()
