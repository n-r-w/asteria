from .models import make_imported_bucket


def consume_relative_bucket() -> str:
    return make_imported_bucket("pkg").label
