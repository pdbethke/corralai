#![deny(warnings)]
// Rust inline-landing fixture: src/lib.rs under #![deny(warnings)], as a crate
// that denies warnings (or builds with RUSTFLAGS=-Dwarnings) writes it; the
// landed block's ancestor glob must not be the warning that breaks the build.
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
