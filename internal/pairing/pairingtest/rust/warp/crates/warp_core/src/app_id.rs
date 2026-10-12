// Excerpt of crates/warp_core/src/app_id.rs, Warp fork @ 325d4d4701b41feb272487e89a6cf02bfba9f194
// (lines 88-90 verbatim; the rest of the file is elided).
pub struct AppId;

#[cfg(test)]
#[path = "app_id_tests.rs"]
mod tests;
