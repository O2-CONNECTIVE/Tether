"""Closed diagnostic phase record; partial/unknown data never authorizes continuation."""
import json
from pathlib import Path
import stat

SLOTS = ("environment", "controller")
EVENTS = ("completion-closure-enter", "send-result", "closure-guard-drop")
TOP = {"schema", "variant", "purpose", "registered", "sealed", "sealElapsedUs", "inFlight", "lossDetected",
       "scopeAttemptsCapped", "scopes", "samples"}
SCOPE = {"slot", "state", "scopeThreadIsTraceThread", "apartmentAtScopeEntry", "apiEntries",
         "apiGuardDrops", "waitEntries", "waitGuardDrops", "scopeGuardDrops", "ambiguous"}
COUNTS = {"apiEntries", "apiGuardDrops", "waitEntries", "waitGuardDrops", "scopeGuardDrops"}
SAMPLE = {"slot", "event", "state", "seq", "elapsedUs", "phase", "relation", "sentContext", "onScopeThread", "sendSucceeded"}
PHASES = {"outside", "sdk-api", "get-message", "dispatch-message", "unknown"}


def integer(value, maximum=2**53 - 1):
    return type(value) is int and 0 <= value <= maximum


def nullable_bool(value):
    return value is None or type(value) is bool


def enum(value, allowed):
    return type(value) is str and value in allowed


def validate(value):
    if (not isinstance(value, dict) or set(value) != TOP or type(value["schema"]) is not int
            or value["schema"] != 1 or value["variant"] != "wry-sdk-delivery-1"
            or value["purpose"] != "diagnostic-hints-only"
            or any(type(value[k]) is not bool for k in ("registered", "sealed", "lossDetected"))
            or not integer(value["sealElapsedUs"]) or not integer(value["inFlight"], 32) or not integer(value["scopeAttemptsCapped"], 3)
            or not isinstance(value["scopes"], list) or len(value["scopes"]) != 2
            or not isinstance(value["samples"], list) or len(value["samples"]) != 6):
        raise ValueError("DELIVERY_RECORD_REJECTED")
    for i, row in enumerate(value["scopes"]):
        if (not isinstance(row, dict) or set(row) != SCOPE or row["slot"] != SLOTS[i]
                or not enum(row["state"], {"missing", "observed", "interrupted"})
                or not nullable_bool(row["scopeThreadIsTraceThread"])
                or not enum(row["apartmentAtScopeEntry"], {"sta", "main-sta", "other", "unknown"})
                or type(row["ambiguous"]) is not bool
                or any(not integer(row[k], 2) for k in COUNTS)):
            raise ValueError("DELIVERY_RECORD_REJECTED")
    for i, row in enumerate(value["samples"]):
        if (not isinstance(row, dict) or set(row) != SAMPLE or row["slot"] != SLOTS[i // 3]
                or row["event"] != EVENTS[i % 3]
                or not enum(row["state"], {"missing", "observed", "interrupted", "ambiguous"})
                or not enum(row["phase"], PHASES)
                or not enum(row["relation"], {"same-creation", "original-wait", "unknown"})
                or not enum(row["sentContext"], {"sent-context", "not-sent-context", "unknown"})
                or not nullable_bool(row["onScopeThread"]) or not nullable_bool(row["sendSucceeded"])):
            raise ValueError("DELIVERY_RECORD_REJECTED")
        if row["state"] == "observed":
            if (not integer(row["seq"], 6) or row["seq"] == 0 or not integer(row["elapsedUs"])
                    or type(row["onScopeThread"]) is not bool
                    or (i % 3 == 1 and type(row["sendSucceeded"]) is not bool)
                    or (i % 3 != 1 and row["sendSucceeded"] is not None)):
                raise ValueError("DELIVERY_RECORD_REJECTED")
        elif (any(row[k] is not None for k in ("seq", "elapsedUs", "onScopeThread", "sendSucceeded"))
              or row["phase"] != "unknown" or row["relation"] != "unknown" or row["sentContext"] != "unknown"):
            raise ValueError("DELIVERY_RECORD_REJECTED")
    return value


def complete(value):
    validate(value)
    if (not value["registered"] or not value["sealed"] or value["inFlight"] != 0
            or value["lossDetected"] or value["scopeAttemptsCapped"] != 2):
        return False
    if any(s["state"] != "observed" or s["scopeThreadIsTraceThread"] is not True
           or s["apartmentAtScopeEntry"] not in {"sta", "main-sta"} or s["ambiguous"]
           or any(s[k] != 1 for k in COUNTS) for s in value["scopes"]):
        return False
    rows = value["samples"]
    if any(r["state"] != "observed" or r["phase"] == "unknown" or r["relation"] == "unknown" or r["sentContext"] == "unknown"
           or r["onScopeThread"] is not True for r in rows):
        return False
    for i in (0, 3):
        if not rows[i]["seq"] < rows[i + 1]["seq"] < rows[i + 2]["seq"]:
            return False
        if rows[i + 1]["sendSucceeded"] is not True:
            return False
    ordered = sorted(rows, key=lambda r: r["seq"])
    if [r["seq"] for r in ordered] != list(range(1, 7)):
        return False
    if [r["elapsedUs"] for r in ordered] != sorted(r["elapsedUs"] for r in ordered):
        return False
    if any(r["elapsedUs"] > value["sealElapsedUs"] for r in rows):
        return False
    return all((r["phase"] not in {"get-message", "dispatch-message"} or r["relation"] == "original-wait")
               and (r["phase"] != "sdk-api" or r["relation"] == "same-creation") for r in rows)


def read_record(path: Path, collection):
    info = path.lstat()
    if (not isinstance(collection, dict) or collection.get("appStartupFileClosed") is not True
            or not stat.S_ISREG(info.st_mode) or info.st_nlink != 1
            or getattr(info, "st_file_attributes", 0) & 1024 or info.st_size > 8192):
        raise ValueError("DELIVERY_COLLECTION_REJECTED")
    data = path.read_bytes()
    if len(data) > 8192: raise ValueError("DELIVERY_COLLECTION_REJECTED")
    try:
        text = data.decode("utf-8")
        value = json.loads(text)
        if json.dumps(value, separators=(",", ":")) != text:
            raise ValueError("DELIVERY_COLLECTION_REJECTED")
    except (UnicodeError, RecursionError):
        raise ValueError("DELIVERY_COLLECTION_REJECTED") from None
    return validate(value)
