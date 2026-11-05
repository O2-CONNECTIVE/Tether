#!/bin/sh
set -eu
cd "$(dirname "$0")"
: "${GO:?Select the existing reviewed Go1.27.3 executable}"
exec python3 build-bundle.py
