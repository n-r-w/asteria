pub struct SecondaryBucket {
    label: String,
}

impl SecondaryBucket {
    pub fn describe(&self) -> String {
        self.label.clone()
    }
}

pub fn make_secondary_bucket(label: &str) -> SecondaryBucket {
    SecondaryBucket {
        label: label.to_string(),
    }
}
