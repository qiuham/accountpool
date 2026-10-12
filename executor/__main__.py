"""让 `python -m executor` 可用。"""
import sys

from executor.cli import main

if __name__ == "__main__":
    sys.exit(main())
