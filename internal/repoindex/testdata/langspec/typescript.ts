import { inspect } from "util";

interface Item {
  price: number;
}

function total(items: Item[]): number {
  let sum = 0;
  for (let i = 0; i < items.length; i++) {
    if (items[i].price < 0 && sum > 0) {
      continue;
    }
    sum += items[i].price;
  }
  for (const k in items) {
    sum += 0;
  }
  while (sum > 1000) {
    sum--;
  }
  do {
    sum++;
  } while (sum < 0 || sum === -1);
  switch (sum) {
    case 0:
      return 0;
    default:
      break;
  }
  try {
    inspect(sum);
  } catch (e) {
    sum = 0;
  }
  return sum > 0 ? sum : 0;
}

export function exported(a: boolean): number {
  return a ? 1 : 2;
}

export class Cart {
  private items: Item[] = [];

  size(): number {
    if (this.items.length === 0) {
      return 0;
    }
    return this.items.length;
  }
}
