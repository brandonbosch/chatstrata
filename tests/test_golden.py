"""The Python ingester must keep matching spec/golden/expected/.

These files are the contract the Go rewrite is tested against. If a change to
an adapter or the ingester is intentional, regenerate them with
`python scripts/golden.py generate` and review the diff.
"""

import importlib.util
from pathlib import Path

SCRIPT = Path(__file__).resolve().parent.parent / "scripts" / "golden.py"


def test_golden_output_matches():
    spec = importlib.util.spec_from_file_location("golden", SCRIPT)
    golden = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(golden)

    failures = golden.check()
    assert not failures, "golden output changed:\n" + "\n".join(failures)
