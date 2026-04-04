from __future__ import annotations

from advanced import DecoratedBucket


def make_imported_bucket(value: str) -> DecoratedBucket[str]:
    return DecoratedBucket(value)
