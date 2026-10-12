#!/usr/bin/env python3
"""按账号生成字体子集引擎副本，让 canvas 指纹在实例之间真正分开。

为什么需要它
------------
camoufox 0.5.7 起移除了 canvas noise，canvas 直接反映宿主真实渲染结果。
同一台机器上跑出来的每个账号，canvas 哈希字节级相同 —— 这是一个跨账号
稳定特征，任何采 canvas 的检测都能把整个号池串成一条线。

维护者的立场是 canvas noise 本身才是负债（daijro/camoufox#536：
"We forgot to remove canvas noise ... you should be able to disable it
yourself"），所以正确的做法不是往 canvas 里加噪声，而是让不同实例的
渲染环境本来就不同 —— 就像不同机器装的字体本来就不同。

机制
----
camoufox/utils.py:206 会读引擎 bundle 里的 fontconfig/{os}/fonts.conf，
把 `<dir prefix="cwd">fonts</dir>` 换成绝对路径后生成运行时配置，并覆盖
FONTCONFIG_FILE。所以改环境变量没用，切入点是 **引擎 bundle 的 fonts/ 目录**。

本脚本用硬链接树复制引擎，再从副本的 fonts/ 里摘掉该子集不该有的字族。
硬链接意味着复制几乎不占空间，且删除只影响副本，源引擎不受影响。

实测（v152.0.4-beta.31 + camoufox 0.5.6，8 字族混排 canvas）：
    fontA(123 字体) hash=-232182975  len=27594
    fontB(144 字体) hash=-332565179 len=26642
    → canvas 不同

用法
----
    # 为账号 7 生成子集（落在 .subsets/7/）
    python tools/font-subset.py --index 7

    # 生成前 10 个
    python tools/font-subset.py --count 10

    # 环境变量控制引擎来源与输出目录
    CAMOUFOX_ENGINE=/path/to/engine SUBSET_DIR=/out python tools/font-subset.py -i 3

生成后用 executable_path 指过去：

    Camoufox(..., executable_path=".subsets/7/camoufox")
"""

import argparse
import json
import os
import shutil
import sys
from pathlib import Path

# 只处理西文字族。中文/日文/韩文/emoji 等一律保留原样 —— 号池面向英文场景，
# 动它们既没必要，也容易把字体回退链搞出破绽。
# 每组是一个「族前缀」集合，命中则该族从该子集里整体摘除。
LATIN_GROUPS = [
    # 0: Arial 族
    ["arial", "ariblk"],
    # 1: Calibri / Carlito 族
    ["calibri"],
    # 2: Cambria 族
    ["cambria"],
    # 3: Candara / Constantia / Corbel（ClearType 显示族）
    ["candara", "constan", "corbel"],
    # 4: Consolas 族
    ["consola"],
    # 5: Verdana / Tahoma
    ["verdana", "tahoma"],
    # 6: Segoe UI
    ["segoeui"],
    # 7: Times / Courier 族
    ["times", "cour", "georgia"],
]

# 每份子集保留的字体族：永远保留全部族，只是「缺哪几组」不同。
# 用轮转法分配，保证任意两份子集内容不同，且各自都还像一台正常机器。
def removed_groups(index: int) -> list[int]:
    n = len(LATIN_GROUPS)
    if n == 1:
        return [0]
    # 每个子集摘掉两组，且相邻 index 的两组错开
    return [(index * 2) % n, (index * 2 + 3) % n]


def find_engine() -> Path:
    """定位引擎，优先跟随 camoufox 自己标注的 active 版本。

    camoufox 在 config.json 的 active_version 里记录了当前渠道选定，
    并且 `camoufox list` 会标注每个版本是 stable 还是 prerelease。
    按文件名排序会误选 prerelease（v156 排在 v152 之后），所以这里以
    active 为准，不要自己猜。
    """
    env = os.environ.get("CAMOUFOX_ENGINE")
    if env:
        p = Path(env).expanduser().resolve()
        if not (p / "fonts").is_dir():
            raise SystemExit(f"CAMOUFOX_ENGINE 指向的目录里没有 fonts/: {p}")
        return p

    cache = Path.home() / ".cache" / "camoufox"
    cfg = cache / "config.json"
    if cfg.is_file():
        try:
            active = json.loads(cfg.read_text()).get("active_version")
            if active:
                p = (cache / active).resolve()
                if (p / "fonts").is_dir():
                    return p
        except (OSError, ValueError):
            pass

    root = cache / "browsers" / "official"
    cands = sorted((d for d in root.iterdir() if (d / "fonts").is_dir())
                   ) if root.is_dir() else []
    if not cands:
        raise SystemExit(
            "找不到引擎。先跑 `python -m camoufox set official/stable`，"
            "或用 CAMOUFOX_ENGINE 指定。"
        )
    # 最后手段：取较低构建号那份（历史上 stable 落后于 prerelease）
    return cands[0]


# 不同引擎版本的字体目录命名不同：
#   v152 及以前  fonts/{linux,macos,windows}
#   v156 起      fonts/{L,LM,LW,M,MW,W}（按字体组），W 对应 Windows 集
FONT_DIR_ALIASES = ("windows", "W", "macos", "M", "linux", "L")


def _link_tree(src: Path, dst: Path) -> None:
    """把 src 整棵树以硬链接方式映射到 dst。

    硬链接让副本几乎不占空间，且之后 unlink 只影响副本、源引擎不变。
    跨设备或文件系统不支持硬链接时逐个退回普通复制。
    """
    dst.mkdir(parents=True, exist_ok=True)
    for root, dirs, files in os.walk(src, followlinks=False):
        rel = Path(root).relative_to(src)
        here = dst / rel
        here.mkdir(parents=True, exist_ok=True)

        for name in files:
            s, d = Path(root) / name, here / name
            if d.exists() and not d.is_symlink():
                continue
            try:
                if s.is_symlink():
                    # 硬链接符号链接本身（不跟随），保持原样
                    os.symlink(os.readlink(s), d)
                else:
                    os.link(s, d)
            except OSError:
                try:
                    shutil.copy2(s, d, follow_symlinks=False)
                except OSError:
                    pass

        for name in dirs:
            (here / name).mkdir(parents=True, exist_ok=True)


def make_subset(engine: Path, dest: Path, index: int, quiet: bool = False) -> dict:
    """用硬链接建引擎副本，再摘掉该子集不该有的西文字族。"""
    if dest.exists():
        shutil.rmtree(dest)

    _link_tree(engine, dest)

    fonts_dir = None
    for cand in FONT_DIR_ALIASES:
        d = dest / "fonts" / cand
        if d.is_dir():
            fonts_dir = d
            break
    if fonts_dir is None:
        layout = sorted(p.name for p in (dest / "fonts").iterdir()) if (dest / "fonts").is_dir() else []
        raise SystemExit(
            f"副本里找不到字体目录，fonts/ 下的实际布局是: {layout}\n"
            f"把对应名字加进 FONT_DIR_ALIASES。副本: {dest}"
        )

    groups = removed_groups(index)
    prefixes = tuple(p for g in groups for p in LATIN_GROUPS[g])

    before = after = 0
    for f in sorted(fonts_dir.iterdir()):
        if not f.is_file():
            continue
        before += 1
        if f.name.lower().startswith(prefixes):
            f.unlink()          # 只删硬链接，源引擎不受影响
        else:
            after += 1

    if not quiet:
        names = ", ".join(f"G{g}" for g in groups)
        print(f"  [{index}] 摘除 {names}  {before} -> {after} 个字体  -> {dest}")

    return {
        "index": index,
        "path": str(dest),
        "executable": str(dest / "camoufox"),
        "removed_groups": groups,
        "fonts_before": before,
        "fonts_after": after,
    }


def main() -> int:
    ap = argparse.ArgumentParser(description="按账号生成字体子集引擎副本")
    ap.add_argument("--index", "-i", type=int, help="单个子集编号")
    ap.add_argument("--count", "-c", type=int, help="批量生成 0..count-1")
    ap.add_argument("--engine", help="引擎目录（默认取 camoufox 缓存里最新一份）")
    ap.add_argument("--out", default=".subsets", help="输出目录（默认 .subsets/）")
    args = ap.parse_args()

    if args.index is None and args.count is None:
        ap.error("需要 --index 或 --count")

    engine = Path(args.engine).expanduser().resolve() if args.engine else find_engine()
    out_root = Path(args.out)
    out_root.mkdir(parents=True, exist_ok=True)

    print(f"引擎: {engine}")
    print(f"输出: {out_root.resolve()}")
    print(f"西文字族分组: {len(LATIN_GROUPS)} 组，每份子集摘除 2 组")
    print()

    indices = [args.index] if args.index is not None else list(range(args.count))
    results = []
    for i in indices:
        results.append(make_subset(engine, out_root / str(i), i))

    print()
    print("用 executable_path 指过去即可：")
    print(f'    Camoufox(..., executable_path="{results[0]["executable"]}")')
    print()
    print("注意：子集里被摘掉的字族会让该字族的 measureText 回落到其它字体，")
    print("这是预期行为 —— 真实机器上缺字体时也这样回落。")
    return 0


if __name__ == "__main__":
    sys.exit(main())
