"""读取 Firefox 会话 Cookie；只读连接包含已提交的 WAL 数据。"""
from contextlib import closing
from pathlib import Path
import sqlite3


def read_session_cookies(bundle: Path) -> dict[str, str]:
    db = bundle / "profile" / "cookies.sqlite"
    if not db.is_file():
        return {}
    with closing(sqlite3.connect(db.resolve().as_uri() + "?mode=ro", uri=True)) as connection:
        return dict(connection.execute(
            "select name, value from moz_cookies "
            "where host in ('claude.ai','.claude.ai') "
            "and name in ('sessionKey','lastActiveOrg','routingHint')"
        ))
