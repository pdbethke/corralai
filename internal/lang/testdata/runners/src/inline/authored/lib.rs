// corral's authored test for src/lib.rs: the zero branch, and the private helper.
#[test]
fn zero_is_zero() {
    assert_eq!(sign(0), 0);
}

#[test]
fn unit_is_reached() {
    assert_eq!(unit(true), -1);
}
