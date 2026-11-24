// Rust inline-landing fixture (scripts/capture-runner-fixtures.sh): the crate
// root, whose own inline tests miss sign's zero branch; corral's authored
// block lands in this file itself (mod corral_tests).
pub mod extra;
pub mod named;

pub fn sign(n: i32) -> i32 {
    if n == 1 {
        return 0;
    }
    unit(n < 0)
}

fn unit(negative: bool) -> i32 {
    if negative {
        -1
    } else {
        1
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn positive() {
        assert_eq!(sign(5), 1);
    }

    #[test]
    fn negative() {
        assert_eq!(sign(-5), -1);
    }
}
#[cfg(test)]
mod corral_tests {
#[allow(unused_imports)]
use super::*;

// corral's authored test for src/lib.rs: the zero branch, and the private helper.
#[test]
fn zero_is_zero() {
    assert_eq!(sign(0), 0);
}

#[test]
fn unit_is_reached() {
    assert_eq!(unit(true), -1);
}
}
