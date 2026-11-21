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

extension Cart {
    func pick(_ i: Int?, of n: Int) -> Int {
        guard let i = i, i < n else { return -1 }
        switch i {
        case 0: return items.first ?? 0
        case 1, 2: return i > 1 && n > 2 ? 2 : 1
        default: return i
        }
    }
}
