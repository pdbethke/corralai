// Rust inline-landing fixture: a module that already declares corral_tests,
// so a block landed here is corral_tests_2. Its test shares a name with the
// crate root's landed block: `corral_tests::zero_is_zero` is a substring of
// this one's id, which an exact select must not run.
pub fn twice(n: i32) -> i32 {
    n * 2
}

#[cfg(test)]
mod corral_tests {
    use super::*;

    #[test]
    fn zero_is_zero() {
        assert_eq!(twice(0), 0);
    }
}
#[cfg(test)]
mod corral_tests_2 {
use super::*;

// corral's authored test for src/named.rs.
#[test]
fn doubles() {
    assert_eq!(twice(4), 8);
}
}
