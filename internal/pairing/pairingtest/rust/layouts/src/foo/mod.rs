// mod.rs: rustc loads a bare `mod tests;` from tests.rs beside this file.
pub fn foo() -> u8 { 1 }

#[cfg(test)]
mod tests;
