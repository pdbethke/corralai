#include <vector>

struct Item {
    int price;
};

class Cart {
public:
    int size() {
        if (items.empty()) {
            return 0;
        }
        return items.size();
    }

    void clear() {
        items.clear();
    }

private:
    std::vector<Item> items;
};

int total(const Cart &c) {
    return 0;
}
