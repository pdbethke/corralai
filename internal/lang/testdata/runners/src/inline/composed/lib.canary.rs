// Rust inline-landing fixture (scripts/capture-runner-fixtures.sh): the crate
// root, whose own inline tests miss sign's zero branch; corral's authored
// block lands in this file itself (mod corral_tests).
pub mod extra;
pub mod named;

pub fn sign(n: i32) -> i32 {
    if n == 0 {
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

#[test]
fn corral_reach_canary() {
    panic!("corral positive control: this test must fail when it runs");
}
}
