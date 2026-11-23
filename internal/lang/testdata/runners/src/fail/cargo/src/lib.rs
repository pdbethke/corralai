// Rust failing fixture (scripts/capture-runner-fixtures.sh), for cargo test
// and cargo nextest: two failing tests before a passing one, and a nextest
// profile that turns fail-fast off, as a CI profile often does. A run that
// stops at the first failure runs one test (with -j 1) and one that does not
// runs three; a run without the two failing tests (salvage) runs the passing
// one alone.
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
