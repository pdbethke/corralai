// The binary of fail/nextest-edge (see src/lib.rs): a failing test in a
// binary, whose nextest binary id is `nextest-edge::bin/edge-bin`.
fn main() {}

#[cfg(test)]
mod tests {
    #[test]
    fn bin_fails() {
        assert!(false);
    }
}
