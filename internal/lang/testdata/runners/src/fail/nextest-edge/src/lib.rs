// fail/nextest-edge (scripts/capture-runner-fixtures.sh): where nextest's JUnit
// record and its printed lines could disagree. A failing test in the library, a
// binary and an integration test (three binary ids); a test that passes on its
// retry (FLAKY), one that fails every try, one nextest times out and one that
// aborts. .config/nextest.toml retries and times out only these, and turns
// fail-fast off.
#[cfg(test)]
mod tests {
    fn attempt() -> u32 {
        std::env::var("NEXTEST_ATTEMPT").ok().and_then(|a| a.parse().ok()).unwrap_or(1)
    }
    #[test]
    fn lib_fails() {
        assert_eq!(1 + 1, 3);
    }
    #[test]
    fn passes_on_retry() {
        assert!(attempt() > 1, "fails on its first try");
    }
    #[test]
    fn fails_every_try() {
        assert!(attempt() == 0, "fails on try {}", attempt());
    }
    #[test]
    fn times_out() {
        std::thread::sleep(std::time::Duration::from_secs(30));
    }
    #[test]
    fn aborts() {
        std::process::abort();
    }
    #[test]
    fn passes() {}
}
