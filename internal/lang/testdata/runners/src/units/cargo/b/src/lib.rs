pub fn neg(a: i32) -> i32 {
    -a
}

#[cfg(test)]
mod tests {
    #[test]
    fn negates() {
        assert_eq!(super::neg(1), -1);
    }
}
