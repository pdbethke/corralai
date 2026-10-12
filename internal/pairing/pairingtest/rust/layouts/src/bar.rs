// A non-mod.rs file: rustc loads a bare `mod tests;` from bar/tests.rs.
pub fn bar() -> u8 { 2 }

#[cfg(test)]
mod tests;
