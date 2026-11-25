// report/nextest-junit (scripts/capture-runner-fixtures.sh, nextest-wrote-*):
// the binding rule that corral never writes into a project's file, held over
// every shape of nextest config that writes a JUnit report of the project's
// own. Each file under configs/ is copied to .config/nextest.toml for one run;
// configs/other.toml is the operator's --config-file.
#[cfg(test)]
mod tests {
    #[test]
    fn fails() {
        assert_eq!(1 + 1, 3);
    }
    #[test]
    fn passes() {
        assert_eq!(1 + 1, 2);
    }
}
