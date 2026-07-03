#!/usr/bin/env python3
"""Additive 3-way JSON deep-merge driver (git merge driver) for i18n messages/*.json.

Conductor's redesign tasks each ADD their own i18n keys to messages/{tr,en,mt,it}.json.
When a task branch merges against a base that OTHER tasks advanced, git's line-based
merge reports a spurious CONFLICT even though the changes are additive (disjoint keys).
That stalls/loops the merge (root cause of the merge-conflict hotspot).

This driver does a semantic 3-way deep-merge of the JSON objects:
  - keys added on either side are UNIONED (the additive case → always resolves clean),
  - a key changed on only one side takes that side,
  - a key changed on BOTH sides to different leaf values: incoming (theirs) wins
    (rare for namespaced-per-screen i18n; deterministic, never a marker),
  - nested objects merge recursively.
It NEVER writes conflict markers and NEVER emits invalid JSON (json.dump). On ANY
error it exits non-zero so git falls back to the normal (line) conflict — fail-safe.

Git invokes it as:  driver %O %A %B %P   (ancestor, ours→result, theirs, path)
"""
import sys, json


def deep_merge(base, ours, theirs):
    if isinstance(ours, dict) and isinstance(theirs, dict):
        b = base if isinstance(base, dict) else {}
        result = dict(ours)
        for k, tv in theirs.items():
            result[k] = tv if k not in result else deep_merge(b.get(k), result[k], tv)
        return result
    if ours == theirs:
        return ours
    if ours == base:          # only theirs changed
        return theirs
    if theirs == base:        # only ours changed
        return ours
    return theirs             # both changed → incoming task wins (deterministic)


def load(p):
    with open(p, encoding="utf-8") as f:
        return json.load(f)


def main():
    if len(sys.argv) < 4:
        sys.stderr.write("json-merge-driver: need %O %A %B\n")
        return 1
    O, A, B = sys.argv[1], sys.argv[2], sys.argv[3]
    try:
        merged = deep_merge(load(O), load(A), load(B))
    except Exception as e:  # invalid JSON / io → fall back to normal conflict
        sys.stderr.write(f"json-merge-driver: {e}\n")
        return 1
    try:
        with open(A, "w", encoding="utf-8") as f:
            json.dump(merged, f, ensure_ascii=False, indent=2)
            f.write("\n")
    except Exception as e:
        sys.stderr.write(f"json-merge-driver write: {e}\n")
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
