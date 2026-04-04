FIXTURE_STAMP = "stamp"
fixture_counter = 0


class Bucket:
    def __init__(self, value: str) -> None:
        self.value = value

    def describe(self) -> str:
        return self.value


def make_bucket(value: str) -> Bucket:
    return Bucket(value)
