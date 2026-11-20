// add is called by the fixture's tests; sub is not.
public func add(_ a: Int, _ b: Int) -> Int {
    a + b
}

public func sub(_ a: Int, _ b: Int) -> Int {
    a - b
}
