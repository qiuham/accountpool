#!/usr/bin/env python3
"""bundle 指纹的读写：dict <-> browserforge Fingerprint dataclass。

背景
----
camoufox 0.5.7 起 `fingerprint=` 接受普通 dict，0.5.6 及更早只接受
browserforge 的 Fingerprint dataclass（utils.py:361 会直接访问
`fingerprint.navigator.userAgent`）。

号池的 bundle 是在 0.5.7 下用 `dataclasses.asdict()` 落盘的，切到 stable
的 0.5.6 之后必须无损还原 —— **不能重新随机生成**，因为账号已经绑在这份
设备特征上了，换指纹等于换机器，会触发风控。

    from executor.runtime.fingerprint import load_fingerprint
    fp = load_fingerprint("bundles/probe-001/fingerprint.json")
"""

import dataclasses
import json
import typing
from pathlib import Path

from browserforge.fingerprints import Fingerprint


def rebuild(cls, data):
    """把 asdict() 出来的纯 dict 递归还原成嵌套 dataclass。

    只用 typing.get_type_hints 解析出的真实类型，够处理 browserforge 的
    Screen / Navigator / VideoCard / Headers 这几层嵌套。
    """
    if not dataclasses.is_dataclass(cls) or not isinstance(data, dict):
        return data
    hints = typing.get_type_hints(cls)
    kwargs = {}
    for f in dataclasses.fields(cls):
        v = data.get(f.name)
        t = hints.get(f.name)
        if v is None:
            kwargs[f.name] = None
        elif isinstance(t, type) and dataclasses.is_dataclass(t):
            kwargs[f.name] = rebuild(t, v)
        else:
            kwargs[f.name] = v
    return cls(**kwargs)


def load_fingerprint(path) -> Fingerprint:
    """读 bundle 里的 fingerprint.json，返回 Fingerprint dataclass。

    如果文件本来就是 dataclass 形状（缺字段会报错），也照样走还原路径。
    """
    raw = json.loads(Path(path).read_text())
    fp = rebuild(Fingerprint, raw)
    # 校验：还原结果必须能再序列化回完全相同的 dict，否则说明漏了字段
    if dataclasses.asdict(fp) != raw:
        missing = set(raw) - set(dataclasses.asdict(fp))
        changed = {k for k in raw if dataclasses.asdict(fp).get(k) != raw[k]}
        raise ValueError(
            f"指纹还原不完整: 缺 {sorted(missing) or '无'}，变 {sorted(changed) or '无'}"
        )
    return fp


def dump_fingerprint(fp, path) -> None:
    """把 Fingerprint 落盘成 bundle 用的 JSON。"""
    Path(path).write_text(
        json.dumps(dataclasses.asdict(fp), indent=2, ensure_ascii=False)
    )


if __name__ == "__main__":
    import sys
    p = sys.argv[1] if len(sys.argv) > 1 else "bundles/probe-001/fingerprint.json"
    fp = load_fingerprint(p)
    print(f"✓ {p}")
    print(f"  screen : {fp.screen.width}x{fp.screen.height}")
    print(f"  cores  : {fp.navigator.hardwareConcurrency}")
    print(f"  ua     : {fp.navigator.userAgent}")
    print(f"  locale : {fp.navigator.language}")
