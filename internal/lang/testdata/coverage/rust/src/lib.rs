// The fixture for rustPlugin's coverage reporter: calc is tested in part,
// dead is compiled and never called, shape has no executable line, and
// unloaded.rs is in no module tree, so nothing ever builds it.
pub mod calc;
pub mod dead;
pub mod shape;
