from __future__ import annotations

from collections.abc import AsyncIterator, Callable, Iterator
from typing import Generic, TypeAlias, TypeVar

ValueT = TypeVar("ValueT", bound=str)
BucketPair: TypeAlias = tuple[str, str]


class DecoratedBucket(Generic[ValueT]):
    def __init__(self, value: ValueT) -> None:
        self._value = value

    @property
    def label(self) -> ValueT:
        return self._value

    @classmethod
    def from_parts(cls, parts: BucketPair) -> DecoratedBucket[str]:
        left, right = parts
        return cls(f"{left}:{right}")

    @staticmethod
    def build_default() -> DecoratedBucket[str]:
        return DecoratedBucket("default")


class DerivedBucket(DecoratedBucket[str]):
    def render(self) -> str:
        return self.label.upper()


def choose_bucket(value: str) -> DecoratedBucket[str]:
    match value:
        case "joined":
            return DecoratedBucket.from_parts(("left", "right"))
        case _:
            return DecoratedBucket.build_default()


def build_labeler(prefix: str) -> Callable[[str], str]:
    def apply(value: str) -> str:
        return f"{prefix}:{value}"

    return apply


def collect_labels(values: list[str]) -> list[str]:
    labeler = build_labeler("seen")
    return [labeler(value) for value in values]


async def iter_labels(values: list[str]) -> AsyncIterator[str]:
    for value in values:
        yield value


async def drain_labels(values: list[str]) -> list[str]:
    return [value async for value in iter_labels(values)]


def sync_labels(values: list[str]) -> Iterator[str]:
    for value in values:
        yield value
