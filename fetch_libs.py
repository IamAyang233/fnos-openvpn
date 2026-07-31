#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
抓取 openvpn 运行所需的共享库，打包进 fnos/app/lib/，实现 FPK 0 依赖：
不依赖飞牛宿主机上是否安装了 libssl3 / liblzo2 等库。

做法：
1. 纯 Python 解析 ELF 的 DT_NEEDED（含 PT_LOAD 的 vaddr->offset 映射）。
2. 按 lib 名 -> Debian 包 映射，从 Debian bookworm 官方源拉取对应 .deb。
3. 解 ar + 解 data.tar.xz，抽出 SONAME 文件写入 fnos/app/lib/。
4. 对抽出的每个 .so 再递归解析依赖，闭包直到只剩 libc/libpthread 等系统基础库。

所有二进制均来自 Debian 官方源（与飞牛同源 glibc），可信且版本一致。
"""
import os
import sys
import io
import re
import lzma
import gzip
import struct
import tarfile
import urllib.request
import urllib.error

PROXY = "http://192.168.100.254:7890"
os.environ.setdefault("HTTP_PROXY", PROXY)
os.environ.setdefault("HTTPS_PROXY", PROXY)

ROOT = os.path.dirname(os.path.abspath(__file__))
APP_BIN = os.path.join(ROOT, "fnos", "app", "bin", "openvpn")
LIBDIR = os.path.join(ROOT, "fnos", "app", "lib")
CACHE = os.path.join(ROOT, ".deb_cache")
os.makedirs(LIBDIR, exist_ok=True)
os.makedirs(CACHE, exist_ok=True)

# lib 基础名 -> (Debian 源码首字母目录, 二进制包名, 候选 deb URL)
# lib 基础名 -> Debian 二进制包名（用 Packages 索引解析权威 deb URL，不再猜版本号）
LIB2PKG = {
    "libssl.so.3": "libssl3",
    "libcrypto.so.3": "libssl3",
    "liblzo2.so.2": "liblzo2-2",
    "liblz4.so.1": "liblz4-1",
    "libcap-ng.so.0": "libcap-ng0",
    "libsystemd.so.0": "libsystemd0",
    "libcap.so.2": "libcap2",
    "libpam.so.0": "libpam0g",
    "libselinux.so.1": "libselinux1",
    "liblzma.so.5": "liblzma5",
    "libzstd.so.1": "libzstd1",
    "libgcrypt.so.20": "libgcrypt20",
    "libgpg-error.so.0": "libgpg-error0",
    "libpkcs11-helper.so.1": "libpkcs11-helper1",
    "libnl-3.so.200": "libnl-3-200",
    "libnl-genl-3.so.200": "libnl-genl-3-200",
}

# 包名 -> deb Filename 的索引缓存（来自 Debian bookworm main 的 Packages 索引）
_PKG_INDEX = None


def load_pkg_index():
    global _PKG_INDEX
    if _PKG_INDEX is not None:
        return _PKG_INDEX
    cached = os.path.join(CACHE, "Packages.xz")
    if os.path.exists(cached) and os.path.getsize(cached) > 1000:
        raw = open(cached, "rb").read()
    else:
        idx_url = "http://deb.debian.org/debian/dists/bookworm/main/binary-amd64/Packages.xz"
        print("  下载 Packages 索引...")
        req = urllib.request.Request(idx_url, headers={"User-Agent": "Mozilla/5.0"})
        with urllib.request.urlopen(req, timeout=180) as r:
            raw = r.read()
        open(cached, "wb").write(raw)
    text = lzma.decompress(raw).decode("utf-8", "ignore")
    _PKG_INDEX = {}
    for stanza in text.split("\n\n"):
        name = fn = None
        for line in stanza.split("\n"):
            if line.startswith("Package: "):
                name = line[9:].strip()
            elif line.startswith("Filename: "):
                fn = line[10:].strip()
        if name and fn:
            _PKG_INDEX[name] = fn
    print("  索引包含 %d 个包" % len(_PKG_INDEX))
    return _PKG_INDEX

SYSTEM_LIBS = {
    "libc.so.6", "libpthread.so.0", "libdl.so.2",
    "libm.so.6", "librt.so.1", "ld-linux-x86-64.so.2",
}


def vaddr_to_off(segments, vaddr):
    for (vaddr0, off0, filesz) in segments:
        if vaddr0 <= vaddr < vaddr0 + filesz:
            return off0 + (vaddr - vaddr0)
    return None


def parse_needed(path):
    with open(path, "rb") as f:
        data = f.read()
    if data[:4] != b"\x7fELF":
        raise SystemExit("不是 ELF: %s" % path)
    ei_class = data[4]
    ei_data = data[5]
    endian = "<" if ei_data == 1 else ">"
    is64 = (ei_class == 2)
    if is64:
        e_phoff = struct.unpack(endian + "Q", data[0x20:0x28])[0]
        e_phentsize = struct.unpack(endian + "H", data[0x36:0x38])[0]
        e_phnum = struct.unpack(endian + "H", data[0x38:0x3a])[0]
    else:
        e_phoff = struct.unpack(endian + "I", data[0x1c:0x20])[0]
        e_phentsize = struct.unpack(endian + "H", data[0x2a:0x2c])[0]
        e_phnum = struct.unpack(endian + "H", data[0x2c:0x2e])[0]

    segments = []
    dyn = None
    for i in range(e_phnum):
        off = e_phoff + i * e_phentsize
        p_type = struct.unpack(endian + "I", data[off:off + 4])[0]
        if is64:
            p_offset = struct.unpack(endian + "Q", data[off + 8:off + 16])[0]
            p_vaddr = struct.unpack(endian + "Q", data[off + 16:off + 24])[0]
            p_filesz = struct.unpack(endian + "Q", data[off + 32:off + 40])[0]
        else:
            p_offset = struct.unpack(endian + "I", data[off + 4:off + 8])[0]
            p_vaddr = struct.unpack(endian + "I", data[off + 8:off + 12])[0]
            p_filesz = struct.unpack(endian + "I", data[off + 16:off + 20])[0]
        if p_type == 1:  # PT_LOAD
            segments.append((p_vaddr, p_offset, p_filesz))
        elif p_type == 2:  # PT_DYNAMIC
            dyn = (p_offset, p_filesz)

    if dyn is None:
        return []
    dyn_off, dyn_size = dyn
    esz = 16 if is64 else 8
    DT_NEEDED = 1
    DT_STRTAB = 5
    strtab = 0
    needed_offs = []
    pos = dyn_off
    end = dyn_off + dyn_size
    while pos + esz <= end:
        if is64:
            d_tag = struct.unpack(endian + "q", data[pos:pos + 8])[0]
            d_val = struct.unpack(endian + "Q", data[pos + 8:pos + 16])[0]
        else:
            d_tag = struct.unpack(endian + "i", data[pos:pos + 4])[0]
            d_val = struct.unpack(endian + "I", data[pos + 4:pos + 8])[0]
        if d_tag == 0:
            break
        if d_tag == DT_STRTAB:
            strtab = d_val
        elif d_tag == DT_NEEDED:
            needed_offs.append(d_val)
        pos += esz

    strtab_off = vaddr_to_off(segments, strtab)
    needed = []
    for no in needed_offs:
        if strtab_off is None:
            continue
        s = strtab_off + no
        e = data.find(b"\x00", s)
        needed.append(data[s:e].decode("utf-8", "ignore"))
    return needed


def download(url):
    fn = os.path.join(CACHE, os.path.basename(url.split("?")[0]))
    if os.path.exists(fn) and os.path.getsize(fn) > 1000:
        return fn
    print("  download:", url)
    req = urllib.request.Request(url, headers={"User-Agent": "Mozilla/5.0"})
    with urllib.request.urlopen(req, timeout=120) as r:
        body = r.read()
    with open(fn, "wb") as f:
        f.write(body)
    return fn


def resolve_deb_url(lib):
    pkg = LIB2PKG.get(lib)
    if not pkg:
        return None
    idx = load_pkg_index()
    fn = idx.get(pkg)
    if not fn:
        print("  (索引中未找到包 %s)" % pkg)
        return None
    return "http://deb.debian.org/debian/" + fn


def extract_so(deb_path, libname):
    with open(deb_path, "rb") as f:
        data = f.read()
    if data[:8] != b"!<arch>\n":
        raise SystemExit("不是 ar 包: %s" % deb_path)
    off = 8
    data_member = None
    while off + 60 <= len(data):
        hdr = data[off:off + 60]
        name = hdr[0:16].decode("utf-8", "ignore").strip().rstrip("/")
        try:
            size = int(hdr[48:58].decode("ascii").strip())
        except ValueError:
            break
        off += 60
        content = data[off:off + size]
        off += size
        if size % 2 == 1:
            off += 1
        if name.startswith("data.tar"):
            data_member = (name, content)
            break
    if data_member is None:
        return None
    n, c = data_member
    if n.endswith(".xz"):
        raw = lzma.decompress(c)
    elif n.endswith(".gz"):
        raw = gzip.decompress(c)
    elif n.endswith(".zst"):
        raise SystemExit("不支持 zst: %s" % deb_path)
    else:
        raw = c
    with tarfile.open(fileobj=io.BytesIO(raw)) as tf:
        import posixpath
        def norm(p):
            p = p.lstrip("./")
            return p.lstrip("/")
        members = {norm(m.name): m for m in tf.getmembers()}
        # 找到 basename 匹配的成员（真实文件或指向真实文件的符号链接）
        target_name = None
        for nm, m in members.items():
            if posixpath.basename(nm) == libname:
                target_name = nm
                break
        if target_name is None:
            return None
        # 跟随符号链接，定位真正的普通文件
        seen = set()
        cur = target_name
        while cur in members and (members[cur].issym() or members[cur].islnk()):
            link = members[cur].linkname
            if link.startswith("/"):
                link = norm(link)
            else:
                link = norm(posixpath.join(posixpath.dirname(cur), link))
            if link in seen:
                break
            seen.add(link)
            cur = link
        if cur in members and members[cur].isfile():
            return tf.extractfile(members[cur]).read()
    return None


def main():
    collected = {}
    visited = set()
    queue = [(APP_BIN, parse_needed(APP_BIN))]
    print("openvpn NEEDED:", queue[0][1])
    while queue:
        binpath, libs = queue.pop(0)
        for lib in libs:
            if lib in SYSTEM_LIBS:
                continue
            if lib in collected or lib in visited:
                continue
            visited.add(lib)
            url = resolve_deb_url(lib)
            if not url:
                raise SystemExit("!! 无法解析 %s 的 deb 包，请补充 LIB2PKG" % lib)
            deb = download(url)
            so = extract_so(deb, lib)
            if so is None:
                raise SystemExit("!! 在 %s 中未找到 %s" % (os.path.basename(deb), lib))
            out = os.path.join(LIBDIR, lib)
            with open(out, "wb") as f:
                f.write(so)
            collected[lib] = out
            print("  打包:", lib, "(%d bytes)" % len(so))
            sub = parse_needed(out)
            if sub:
                queue.append((out, sub))

    print("\n完成，共打包 %d 个库到 fnos/app/lib/:" % len(collected))
    for k in sorted(collected):
        print("  -", k)


if __name__ == "__main__":
    main()
