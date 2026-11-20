import Foundation

protocol Priced {
    func price() -> Int
}

func total(_ items: [Int]) -> Int {
    if items.isEmpty {
        return 0
    }
    return items.reduce(0, +)
}

class Cart {
    var items: [Int] = []

    func size() -> Int {
        if items.isEmpty {
            return 0
        }
        return items.count
    }

    func clear() {
        items.removeAll()
    }
}
