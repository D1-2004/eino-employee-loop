#!/usr/bin/env python3
"""Compatibility entry point for the Go local replay evaluator.

All case execution and evidence validation live in app/eval; Go is required.
"""
from pathlib import Path
import os
import sys

ROOT = Path(__file__).resolve().parents[1]

if __name__ == "__main__":
    os.chdir(ROOT)
    os.execvp("go", ["go", "run", "./app/eval", *sys.argv[1:]])
