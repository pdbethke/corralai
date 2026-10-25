// Compiled into the crate and never called by any test.
pub fn never(n: i64) -> i64 {
    let doubled = n * 2;
    doubled + 1
}
