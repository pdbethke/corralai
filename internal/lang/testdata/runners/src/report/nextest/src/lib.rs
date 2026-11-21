// Report fixture (scripts/capture-runner-fixtures.sh): a project config that hides the lines corral reads, and the runner row's report flags that bring them back.
#[cfg(test)]
mod tests {
    #[test]
    fn ok() {}
    #[test]
    fn adds() {
        assert_eq!(1 + 1, 3);
    }
}
