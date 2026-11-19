"""復旧用の監視プロセスであり、改ざん耐性のある失効管理機構ではない。

バックエンドによる強制失効が実装されるまで、リリース制御で新規セッションを禁止する。
このプロセスは旧セッションの後処理を改善するが、同一ユーザー権限による悪意ある終了操作には
耐えられず、リモートの TCP ストリームが閉じたことも証明できない。"""
from __future__ import annotations
import math
import os
from pathlib import Path
import time
from rescue_core import (RescueError, atomic_json_write, data_dir,
    remove_session_state_if_name, resolve_upterm, stop_session, SESSION_NAME_PATTERN)


def watchdog_main(name: str, deadline_string: str, external: str, ready_path: str | None = None) -> int:
    if not SESSION_NAME_PATTERN.fullmatch(name):
        return 2
    try:
        deadline = float(deadline_string)
        if not math.isfinite(deadline) or deadline > time.time() + 3 * 3600:
            return 2
        exe = resolve_upterm("" if external == "-" else external)
        monotonic_deadline = time.monotonic() + max(0, deadline - time.time())
        if ready_path:
            expected = data_dir() / "sessions" / (name + ".watchdog-ready.json")
            if Path(ready_path).resolve() != expected.resolve():
                return 2
            atomic_json_write(expected, {"name": name, "pid": os.getpid(), "deadline": deadline})
        # 時計が巻き戻っても稼働中の監視期限は延長しない。時計が進んだ場合は期限が早まる。
        # 期限切れの旧セッションは直ちに停止する。古すぎるという理由で停止を拒否しない。
        while True:
            remaining = min(deadline - time.time(), monotonic_deadline - time.monotonic())
            if remaining <= 0:
                break
            time.sleep(min(remaining, 1))
        for attempt in range(5):
            try:
                stop_session(exe, name)
                remove_session_state_if_name(name)
                return 0
            except (RescueError, OSError):
                if attempt == 4:
                    raise
                time.sleep(min(2 ** attempt, 8))
    except (ValueError, RescueError, OSError) as exc:
        log_path = data_dir() / "watchdog.log"
        try:
            log_path.parent.mkdir(parents=True, exist_ok=True)
            with log_path.open("a", encoding="utf-8") as out:
                out.write(f"{time.strftime('%Y-%m-%d %H:%M:%S')} stop {name} UNCONFIRMED ({type(exc).__name__}); state retained\n")
        except OSError:
            # ログ記録の失敗を停止成功として扱わず、非ゼロの終了結果を保持する。
            pass
        return 1
    return 1
