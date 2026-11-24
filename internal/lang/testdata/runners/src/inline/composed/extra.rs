// Rust inline-landing fixture: a module that is not the crate root, whose own
// inline tests miss clamp's upper bound; its ids carry the `extra::` path.
pub fn clamp(n: i32, lo: i32, hi: i32) -> i32 {
    if n < lo {
        lo
    } else if n > hi {
        hi
    } else {
        within(n)
    }
}

fn within(n: i32) -> i32 {
    n
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn below() {
        assert_eq!(clamp(-3, 0, 10), 0);
    }

    #[test]
    fn inside() {
        assert_eq!(clamp(4, 0, 10), 4);
    }
}
#[cfg(test)]
mod corral_tests {
#[allow(unused_imports)]
use super::*;

// corral's authored test for src/extra.rs, wrapped as a writer may wrap
// one (the landing unwraps it): the upper bound, and the private helper.

#[test]
fn above() {
    assert_eq!(clamp(30, 0, 10), 10);
}

#[test]
fn within_is_identity() {
    assert_eq!(within(7), 7);
}
}
