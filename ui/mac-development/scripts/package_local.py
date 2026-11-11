#!/usr/bin/env python3
"""Package existing local development binaries. Does not run/sign/install them."""
import hashlib
import pathlib
import plistlib
import shutil
import sys

ROOT = pathlib.Path(__file__).resolve().parents[1]


def package(gui: pathlib.Path, resident: pathlib.Path, output: pathlib.Path) -> None:
    for source in (gui, resident):
        if source.is_symlink() or not source.is_file() or source.stat().st_size > 128 * 1024 * 1024:
            raise ValueError("expected bounded regular binary")
    if output.exists() or output.is_symlink():
        raise ValueError("output must not exist")
    macos = output / "Contents" / "MacOS"
    resources = output / "Contents" / "Resources"
    macos.mkdir(parents=True)
    resources.mkdir()
    for source, name in ((gui, "TetherDevelopment"), (resident, "tether-dev-resident")):
        target = macos / name
        shutil.copyfile(source, target)
        target.chmod(0o755)
    digest = hashlib.sha256((macos / "tether-dev-resident").read_bytes()).hexdigest()
    (resources / "resident.sha256").write_text(digest + "\n", encoding="ascii")
    with (output / "Contents" / "Info.plist").open("wb") as stream:
        plistlib.dump({"CFBundleExecutable": "TetherDevelopment",
                      "CFBundleIdentifier": "com.o2connective.tether.development.gui",
                      "CFBundleName": "Tether Development", "CFBundleDisplayName": "Tether Development",
                      "CFBundlePackageType": "APPL", "CFBundleVersion": "1",
                      "CFBundleShortVersionString": "0.1.0", "LSMinimumSystemVersion": "13.0",
                      "NSHighResolutionCapable": True}, stream)


if __name__ == "__main__":
    if len(sys.argv) != 1:
        raise SystemExit("No input overrides. Build the fixed local outputs first.")
    package(ROOT / ".build/debug/TetherDevelopment", ROOT / ".build/tether-dev-resident",
            ROOT / ".build/local/Tether Development.app")
