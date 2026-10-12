// Excerpt of crates/warp_cli/src/local_control/mod.rs, Warp fork @ 325d4d4701b41feb272487e89a6cf02bfba9f194
// (lines 944-946 verbatim; the rest of the file is elided). The ../ is
// relative to this file's directory, so the test is
// crates/warp_cli/src/local_control_tests.rs.
pub(crate) fn serve() {}

#[cfg(test)]
#[path = "../local_control_tests.rs"]
mod tests;
