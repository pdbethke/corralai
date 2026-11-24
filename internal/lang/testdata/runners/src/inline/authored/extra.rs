#[cfg(test)]
mod tests {
    // corral's authored test for src/extra.rs, wrapped as a writer may wrap
    // one (the landing unwraps it): the upper bound, and the private helper.
    use super::*;

    #[test]
    fn above() {
        assert_eq!(clamp(30, 0, 10), 10);
    }

    #[test]
    fn within_is_identity() {
        assert_eq!(within(7), 7);
    }
}
