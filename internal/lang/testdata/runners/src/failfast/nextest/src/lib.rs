// Fail-fast fixture (scripts/capture-runner-fixtures.sh): a profile that turns
// nextest's fail-fast off, as a CI profile often does, and two failing tests
// before a passing one, so a run that stops at the first failure runs one test
// (with -j 1) and one that does not runs three.
#[cfg(test)]
mod tests {
    #[test]
    fn a_fails() {
        assert_eq!(1 + 1, 3);
    }
    #[test]
    fn b_fails() {
        assert_eq!(1 + 1, 4);
    }
    #[test]
    fn c_passes() {
        assert_eq!(1 + 1, 2);
    }
}
