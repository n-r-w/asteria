pub mod references;
pub mod reexports;

pub use reexports::AliasBucket as ReexportedBucket;
pub use reexports::build_alias_bucket as reexported_make_bucket;

#[macro_export]
macro_rules! exported_bucket_macro {
    () => {
        $crate::make_bucket("macro")
    };
}

pub const FIXTURE_STAMP: &str = "rust-fixture";
pub static FIXTURE_COUNTER: usize = 7;

pub mod nested {
    pub struct NestedBucket {
        label: String,
    }

    impl NestedBucket {
        pub fn describe(&self) -> String {
            self.label.clone()
        }
    }
}

pub mod advanced {
    pub trait DisplayLabel {
        fn render(&self) -> String;
    }

    pub enum BucketState {
        Ready,
        Missing,
    }

    pub struct TupleBucket(pub String, pub usize);
    pub struct UnitBucket;

    pub type BucketAlias = TupleBucket;

    pub struct GenericBucket<'a, const N: usize, T> {
        pub values: &'a [T; N],
    }

    impl<'a, const N: usize, T> GenericBucket<'a, N, T> {
        pub fn first(&self) -> Option<&T> {
            self.values.first()
        }
    }

    impl DisplayLabel for TupleBucket {
        fn render(&self) -> String {
            self.0.clone()
        }
    }

    pub(crate) struct CrateVisibleBucket {
        pub label: String,
    }

    pub const GENERIC_WIDTH: usize = 2;
    pub static GLOBAL_LABEL: &str = "global";

    pub fn bucket_from_parts<'a, const N: usize>(values: &'a [String; N]) -> GenericBucket<'a, N, String> {
        GenericBucket { values }
    }

    pub fn alias_bucket() -> BucketAlias {
        TupleBucket(String::from("alias"), GENERIC_WIDTH)
    }

    pub async fn load_label() -> String {
        let prefix = || GLOBAL_LABEL.to_string();
        prefix()
    }

    pub fn pattern_label(input: (String, Vec<String>)) -> String {
        let (head, tail) = input;
        let Some(first_tail) = tail.first() else {
            return head;
        };

        format!("{head}:{first_tail}")
    }
}

pub struct Bucket {
    label: String,
}

impl Bucket {
    pub fn describe(&self) -> String {
        self.label.clone()
    }
}

pub fn make_bucket(label: &str) -> Bucket {
    Bucket {
        label: label.to_string(),
    }
}

pub fn bucket_in_lib() -> String {
    let bucket = make_bucket("primary");
    bucket.describe()
}

pub fn use_bucket_in_same_file() -> String {
    let first = bucket_in_lib();
    let second = bucket_in_lib();
    format!("{first}:{second}")
}
