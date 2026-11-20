// Compiled into the module and never called by any test.
public func never(_ n: Int) -> Int {
    let doubled = n * 2
    return doubled + 1
}
