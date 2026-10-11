#include <stdio.h>

struct cart {
    int items[10];
    int n;
};

int total(struct cart *c) {
    int sum = 0;
    for (int i = 0; i < c->n; i++) {
        if (c->items[i] > 0 && sum >= 0) {
            sum += c->items[i];
        }
    }
    return sum;
}

static void clear(struct cart *c) {
    c->n = 0;
}
