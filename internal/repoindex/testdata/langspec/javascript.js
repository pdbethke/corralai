const util = require("util");

function total(items) {
  let sum = 0;
  for (let i = 0; i < items.length; i++) {
    if (items[i] < 0 && sum > 0) {
      continue;
    }
    sum += items[i];
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
      return null;
    default:
      break;
  }
  try {
    util.inspect(sum);
  } catch (e) {
    sum = 0;
  }
  return sum > 0 ? sum : 0;
}

export function exported(a) {
  return a ? 1 : 2;
}

class Cart {
  constructor() {
    this.items = [];
  }

  size() {
    if (this.items.length === 0) {
      return 0;
    }
    return this.items.length;
  }
}
