"""Closed-schema diagnostic evidence; it never authorizes native success or cleanup."""
from __future__ import annotations

import json
from pathlib import Path
import stat

STAGES = tuple(f"{part}-{stage}" for part in ("environment", "controller")
               for stage in ("enter", "api-ok", "result-sent", "wait-returned"))
KEYS = {"schema", "variant", "purpose", "registered", "sealed", "sealElapsedUs",
        "attempted", "recorded", "dropped", "inFlight", "events"}


def integer(value: object) -> bool:
    return type(value) is int and 0 <= value <= 2**53 - 1


def validate(value: object) -> dict[str, object]:
    if not isinstance(value, dict) or set(value) != KEYS:
        raise ValueError("WRY_PROBE_RECORD_REJECTED")
    if (type(value["schema"]) is not int or value["schema"] != 1
            or value["variant"] != "wry-completion-probe-1"
            or value["purpose"] != "diagnostic-hints-only"
            or value["registered"] is not True or value["sealed"] is not True
            or type(value["dropped"]) is not int or value["dropped"] != 0
            or type(value["inFlight"]) is not int or value["inFlight"] != 0
            or not integer(value["sealElapsedUs"])):
        raise ValueError("WRY_PROBE_RECORD_REJECTED")
    events = value["events"]
    if (not isinstance(events, list) or len(events) > 8
            or type(value["attempted"]) is not int or value["attempted"] != len(events)
            or type(value["recorded"]) is not int or value["recorded"] != len(events)):
        raise ValueError("WRY_PROBE_RECORD_REJECTED")
    seen: set[str] = set()
    elapsed = 0
    for index, row in enumerate(events):
        if (not isinstance(row, dict) or set(row) != {"seq", "stage", "elapsedUs", "onTraceThread"}
                or type(row["seq"]) is not int or row["seq"] != index + 1
                or row["stage"] not in STAGES or row["stage"] in seen
                or not integer(row["elapsedUs"]) or not elapsed <= row["elapsedUs"] <= value["sealElapsedUs"]
                or type(row["onTraceThread"]) is not bool):
            raise ValueError("WRY_PROBE_RECORD_REJECTED")
        stage = row["stage"]
        part, action = stage.split("-", 1)
        if ((action != "enter" and f"{part}-enter" not in seen)
                or (part == "controller" and "environment-wait-returned" not in seen)
                or (action == "wait-returned" and f"{part}-api-ok" not in seen)
                or (action != "result-sent" and row["onTraceThread"] is not True)
                or (action not in ("result-sent", "enter") and f"{part}-wait-returned" in seen)):
            raise ValueError("WRY_PROBE_RECORD_REJECTED")
        seen.add(stage)
        elapsed = row["elapsedUs"]
    # Callback can run before the immediate API returns. After send unblocks the
    # receiver, its hook can also be scheduled after wait-returned/controller-enter.
    # Missing callback receipt alone is not proof that send/callback never occurred.
    return value


def read_record(path: Path, collection: object) -> dict[str, object]:
    info = path.lstat()
    if (not isinstance(collection, dict) or collection.get("appStartupFileClosed") is not True
            or not stat.S_ISREG(info.st_mode) or info.st_nlink != 1
            or getattr(info, "st_file_attributes", 0) & 1024 or info.st_size > 2048):
        raise ValueError("WRY_PROBE_COLLECTION_REJECTED")
    text = path.read_text(encoding="utf-8")
    if len(text.encode()) > 2048:
        raise ValueError("WRY_PROBE_COLLECTION_REJECTED")
    value = json.loads(text)
    # Reject duplicate keys/whitespace/hidden text rather than normalizing them.
    if json.dumps(value, separators=(",", ":")) != text:
        raise ValueError("WRY_PROBE_COLLECTION_REJECTED")
    return validate(value)
