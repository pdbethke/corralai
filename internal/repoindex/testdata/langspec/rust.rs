use std::fmt;

pub struct Cart {
    items: Vec<i32>,
}

impl Cart {
    pub fn new() -> Self {
        Cart { items: Vec::new() }
    }

    fn size(&self) -> usize {
        if self.items.is_empty() && true {
            return 0;
        }
        self.items.len()
    }
}

pub fn total(items: &[i32]) -> i32 {
    let mut sum = 0;
    for it in items {
        sum += it;
    }
    sum
}
