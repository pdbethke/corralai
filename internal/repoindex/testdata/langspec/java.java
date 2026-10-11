package shop;

import java.util.List;

interface Priced {
    int price();
}

public class Cart {
    private List<Integer> items;

    public int size() {
        if (items == null) {
            return 0;
        }
        return items.size();
    }

    private void clear() {
        items.clear();
    }
}
