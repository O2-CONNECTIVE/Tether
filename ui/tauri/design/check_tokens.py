"""Static token validation, not a behavior-TDD or native-runtime test."""

import colorsys
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent
UI = ROOT.parents[1]
# Mac の palette（Swift）は tokens.css の ref から写した値。名前 → ref の対応は docs/tauri-design-tokens.md。
MAC_PALETTES = (UI / "mac-status/Sources/StatusUI/TetherPalette.swift",
                UI / "mac-development/Sources/TetherDevelopment/TetherPalette.swift")
MAC_REFS = {"canvas": "canvas", "panel": "white", "ink": "ink", "muted": "ink-muted",
            "line": "line", "info": "blue-700", "warning": "amber-800"}
MAC_PAIRS = (("ink", "canvas"), ("ink", "panel"), ("muted", "canvas"), ("muted", "panel"),
             ("info", "canvas"), ("warning", "canvas"))
STATE_ROLES = ("info", "confirmation", "warning", "danger", "neutral")


def read_tokens():
    source = (ROOT / "tokens.css").read_text(encoding="utf-8")
    light = source.split(":root {", 1)[1].split("}", 1)[0]
    pairs = re.findall(r"(--[\w-]+)\s*:\s*([^;]+);", light)
    tokens = dict(pairs)
    assert len(tokens) == len(pairs), "Duplicate token name"
    for name, value in pairs:
        assert name.startswith(("--ref-", "--sys-", "--cmp-")), name
        refs = re.findall(r"var\((--[\w-]+)\)", value)
        if name.startswith("--ref-"):
            assert not refs, "Foundation must contain literal values: " + name
        elif name.startswith("--sys-"):
            assert all(ref.startswith("--ref-") for ref in refs), name
        else:
            # Component metrics may be literal; aliases must target semantic roles.
            assert all(ref.startswith("--sys-") for ref in refs), name
    for file in ("components.css", "preview.css"):
        for ref in re.findall(r"var\((--[\w-]+)\)", (ROOT / file).read_text()):
            assert not ref.startswith("--ref-"), "Bypassed semantic role: " + ref
            assert ref in tokens, "Unknown token: " + ref
    return tokens


def resolve(tokens, name, trail=()):
    assert name in tokens, "Unknown token: " + name
    assert name not in trail, "Token cycle: " + name
    return re.sub(r"var\((--[\w-]+)\)",
                  lambda match: resolve(tokens, match[1], trail + (name,)),
                  tokens[name])


def luminance(color):
    assert re.fullmatch(r"#[0-9a-fA-F]{6}", color), color
    channels = [int(color[i:i + 2], 16) / 255 for i in (1, 3, 5)]
    linear = [c / 12.92 if c <= 0.04045 else ((c + 0.055) / 1.055) ** 2.4
              for c in channels]
    return sum(c * weight for c, weight in zip(linear, (0.2126, 0.7152, 0.0722)))


def contrast(first, second):
    light, dark = sorted((luminance(first), luminance(second)), reverse=True)
    return (light + 0.05) / (dark + 0.05)


def hue_distance(first, second):
    """色相の差（度）。彩度が低い色は色相を持たないので None。"""
    hsv = [colorsys.rgb_to_hsv(*(int(c[i:i + 2], 16) / 255 for i in (1, 3, 5))) for c in (first, second)]
    if min(h[1] for h in hsv) < 0.25:
        return None
    gap = abs(hsv[0][0] - hsv[1][0]) * 360
    return min(gap, 360 - gap)


def check_primary_is_not_a_state_color(tokens):
    """操作の色（primary）を状態の色と兼ねない。同じ値、または色相が近い値を拒否する。"""
    for action in ("action-primary", "action-primary-hover"):
        primary = resolve(tokens, "--sys-" + action)
        for role in STATE_ROLES:
            for part in ("fg", "surface"):
                other = resolve(tokens, "--sys-{}-{}".format(role, part))
                assert other.lower() != primary.lower(), \
                    "Primary action color is shared with a state role: {} / {}-{}".format(action, role, part)
            gap = hue_distance(primary, resolve(tokens, "--sys-{}-fg".format(role)))
            assert gap is None or gap >= 30, \
                "Primary action color is too close to a state role: {} / {}-fg ({:.0f} deg)".format(action, role, gap)


def read_mac_palette(path):
    source = path.read_text(encoding="utf-8")
    block = source.split("// palette:start", 1)[1].split("// palette:end", 1)[0]
    return block, {name: "#" + value.lower()
                   for name, value in re.findall(r"static let (\w+) = 0x([0-9A-Fa-f]{6})(?![0-9A-Fa-f_])", block)}


def check_mac_palette(tokens):
    """Mac の palette が tokens.css の ref と同じ値で、2 つの app で同じであること。"""
    (status_block, status), (development_block, _) = (read_mac_palette(path) for path in MAC_PALETTES)
    assert status_block == development_block, "Mac palette differs between mac-status and mac-development"
    assert set(status) == set(MAC_REFS), "Mac palette names differ from MAC_REFS: " + repr(sorted(status))
    for name, ref in MAC_REFS.items():
        assert status[name] == tokens["--ref-" + ref].lower(), \
            "Mac palette differs from tokens.css: {} ({} vs --ref-{})".format(name, status[name], ref)
    return status


def main():
    markdown_only = "--markdown" in sys.argv[1:]
    tokens = read_tokens()
    check_primary_is_not_a_state_color(tokens)
    mac = check_mac_palette(tokens)
    for name in tokens:
        resolve(tokens, name)
    # Known WCAG reference values check the measurement independently.
    assert contrast("#000000", "#ffffff") == 21
    assert contrast("#155a92", "#155a92") == 1
    pairs = []
    for fg in ("text-primary", "text-secondary"):
        for bg in ("surface-canvas", "surface-default", "surface-subtle"):
            pairs.append((fg, bg, 4.5))
    for role in ("danger", "warning", "info", "confirmation", "neutral"):
        pairs.append((role + "-fg", role + "-surface", 4.5))
    for bg in ("action-primary", "action-primary-hover"):
        pairs.append(("action-on-primary", bg, 4.5))
    for bg in ("danger-action", "danger-hover"):
        pairs.append(("danger-on-action", bg, 4.5))
    pairs.append(("disabled-fg", "disabled-surface", 4.5))
    for bg in ("surface-default", "surface-canvas", "surface-subtle"):
        pairs.append(("border-default", bg, 3))
        pairs.append(("focus-ring", bg, 3))
    pairs.append(("disabled-border", "disabled-surface", 3))
    # リンクは primary を文字色に使う。入力の枠・区切りは操作部品の境界として 3:1。
    for bg in ("surface-default", "surface-canvas", "surface-subtle"):
        pairs.append(("action-primary", bg, 4.5))
        pairs.append(("border-strong", bg, 3))
    for bg in ("surface-default", "surface-canvas"):
        pairs.append(("danger-fg", bg, 4.5))
    rows = []
    failed = []
    for fg, bg, minimum in pairs:
        value = contrast(resolve(tokens, "--sys-" + fg), resolve(tokens, "--sys-" + bg))
        rows.append(("{}".format(fg), bg, value, minimum))
        if value < minimum:
            failed.append((fg, bg, value))
    for fg, bg in MAC_PAIRS:
        value = contrast(mac[fg], mac[bg])
        rows.append(("Mac {}".format(fg), "Mac " + bg, value, 4.5))
        if value < 4.5:
            failed.append(("Mac " + fg, "Mac " + bg, value))
    lines = ["| Foreground / boundary | Background | Contrast | Minimum |", "| --- | --- | ---: | ---: |"]
    lines += ["| {} | {} | {:.2f}:1 | {}:1 |".format(fg, bg, value, minimum) for fg, bg, value, minimum in rows]
    print("\n".join(lines))
    assert not failed, "Contrast below target: " + repr(failed)
    if not markdown_only:
        print("\nPASS: {} tokens; {} contrast pairs. Light theme only. Mac palette: {} colors match tokens.css.".format(
            len(tokens), len(rows), len(mac)))


if __name__ == "__main__":
    main()
