// The #[path] climbs out of the repository: never a pairing.
pub fn escape() -> u8 { 6 }

#[cfg(test)]
#[path = "../../outside_tests.rs"]
mod tests;
