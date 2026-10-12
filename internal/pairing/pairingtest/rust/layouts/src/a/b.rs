// #[path] is relative to THIS file's directory (src/a), so the test is
// src/elsewhere/x.rs: a name no filename convention would guess.
pub fn b() -> u8 { 3 }

#[cfg(test)]
#[path = "../elsewhere/x.rs"]
mod tests;
