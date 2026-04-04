use crate::{exported_bucket_macro, make_bucket, reexported_make_bucket, ReexportedBucket};

pub fn use_bucket() -> String {
    let bucket = make_bucket("secondary");
    bucket.describe()
}

pub fn use_reexported_bucket() -> String {
    let bucket: ReexportedBucket = reexported_make_bucket();
    bucket.describe()
}

pub fn use_exported_bucket_macro() -> String {
    exported_bucket_macro!().describe()
}
