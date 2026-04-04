from fixture import Bucket, make_bucket


def use_bucket() -> str:
    left = Bucket("primary")
    right = make_bucket("secondary")
    return left.describe() + right.describe()
