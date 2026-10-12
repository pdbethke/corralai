// An inline test module: the tests live in this file.
pub fn add(a: u8, b: u8) -> u8 { a + b }

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn adds() {
        assert_eq!(add(1, 2), 3);
    }
}
