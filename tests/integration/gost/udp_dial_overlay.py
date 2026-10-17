"""Layer the isolated QUIC Dial cancellation fix over an existing Go overlay.

Reads source/parent without changing either. Writes only to an empty output
directory; Go source must be a private checkout rather than GOMODCACHE.
"""
from __future__ import annotations
import argparse
import hashlib
import json
from pathlib import Path

OLD = """\t\tif d.sessions[addr] == session {
\t\t\tdelete(d.sessions, addr)
\t\t}
\t\tsession.Close()"""
NEW = """\t\t// A stream-open cancellation belongs to one Dial request. Only evict a
\t\t// dead session, and never remove a newer replacement from the cache.
\t\tif session.IsClosed() && d.sessions[addr] == session {
\t\t\tdelete(d.sessions, addr)
\t\t}"""
CANDIDATE_TEST_PATTERN = "^TestDial(CancellationPreservesSharedSession|DeadSharedSessionReconnects|FailedOldSessionCannotEvictReplacement)$"
# Byte-level gates intentionally reject even an otherwise patchable source drift.
# The existing abort overlay does not alter this file, so its effective version
# must currently be the same pinned, independently tested upstream source.
PINNED_ORIGINAL_SHA256 = "c5da9a9c89340347deade8beb5d3c7c45c6233a0d7a255a0e2443623caf3f481"
ALLOWED_EFFECTIVE_SHA256 = frozenset({PINNED_ORIGINAL_SHA256})

def sha(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()

def build(output: Path, source: Path, parent: dict | None = None,
          *, include_tests: bool = False) -> dict:
    output, source = Path(output).resolve(), Path(source).resolve()
    if output.exists() and any(output.iterdir()):
        raise ValueError("output must be absent or empty")
    target = source / "dialer/quic/dialer.go"
    original_sha256 = sha(target)
    if original_sha256 != PINNED_ORIGINAL_SHA256:
        raise ValueError("original dialer.go SHA256 does not match pinned upstream")
    overlay = (parent or {}).get("overlay", parent or {"Replace": {}})
    mappings = dict(overlay["Replace"])
    effective = target
    for original, replacement in mappings.items():
        if Path(original).resolve() == target:
            if not replacement:
                raise ValueError("parent deletes dialer.go")
            effective = Path(replacement).resolve()
    effective_sha256 = sha(effective)
    if effective_sha256 not in ALLOWED_EFFECTIVE_SHA256:
        raise ValueError("parent effective dialer.go SHA256 is not allowlisted")
    text = effective.read_text(encoding="utf-8-sig")
    if text.count(OLD) != 1:
        raise ValueError("expected exactly one unfixed Dial cleanup block")
    contract = Path(__file__).resolve().parent / "dial_contracts/dialer_cancel_contract_test.go"
    if include_tests and not contract.is_file():
        raise ValueError("contract source is missing")
    output.mkdir(parents=True, exist_ok=True)
    candidate = output / "dialer.go"
    candidate.write_text(text.replace(OLD, NEW, 1), encoding="utf-8", newline="\n")
    # Normalize the one edited key so a parent spelling alias cannot hide it.
    mappings = {k: v for k, v in mappings.items() if Path(k).resolve() != target}
    mappings[str(target)] = str(candidate)
    test_target = source / "dialer/quic/dialer_cancel_test.go"
    if include_tests:
        mappings[str(test_target)] = str(contract)
    result = {
        "overlay": {"Replace": mappings},
        "source": str(source), "effective_input": str(effective),
        "original_input_sha256": original_sha256,
        "pinned_original_sha256": PINNED_ORIGINAL_SHA256,
        "effective_input_sha256": effective_sha256,
        "allowed_effective_sha256": sorted(ALLOWED_EFFECTIVE_SHA256),
        "candidate": str(candidate), "candidate_sha256": sha(candidate),
        "contract": str(contract) if include_tests else None,
        "contract_sha256": sha(contract) if include_tests else None,
        "candidate_test_pattern": CANDIDATE_TEST_PATTERN,
        "scope": "only request-local Dial error cleanup; no transport ownership, 0RTT, or production change",
    }
    (output / "overlay.json").write_text(json.dumps(result["overlay"], indent=2) + "\n", encoding="utf-8")
    (output / "provenance.json").write_text(json.dumps(result, indent=2) + "\n", encoding="utf-8")
    return result

def dial_overlay(output: Path, local_x: Path, parent: dict | None = None) -> dict:
    """Return provenance plus overlay; preserve parent mappings, change only Dial.

    Contracts are deliberately a separate opt-in so production/research builds
    do not gain a test mapping from this layer. All input paths remain read-only.
    """
    return build(output, local_x, parent, include_tests=False)

if __name__ == "__main__":
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--source", type=Path, required=True)
    p.add_argument("--output", type=Path, required=True)
    p.add_argument("--parent", type=Path, help="overlay.json or provenance JSON containing overlay.Replace")
    tests = p.add_mutually_exclusive_group()
    tests.add_argument("--with-tests", action="store_true", help="add the separate Go contract mapping")
    tests.add_argument("--without-tests", action="store_true", help="legacy explicit default")
    a = p.parse_args()
    parent = json.loads(a.parent.read_text(encoding="utf-8-sig")) if a.parent else None
    print(json.dumps(build(a.output, a.source, parent, include_tests=a.with_tests), indent=2))
