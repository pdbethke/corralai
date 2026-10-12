// The #[path] names a file that does not exist: a miss, never a guess.
pub fn missing() -> u8 { 5 }

#[cfg(test)]
#[path = "missing_tests.rs"]
mod tests;
