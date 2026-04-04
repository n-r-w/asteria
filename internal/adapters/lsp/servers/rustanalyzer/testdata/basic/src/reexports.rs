pub struct AliasBucket {
    label: String,
}

impl AliasBucket {
    pub fn describe(&self) -> String {
        self.label.clone()
    }
}

pub fn build_alias_bucket() -> AliasBucket {
    AliasBucket {
        label: String::from("reexported"),
    }
}