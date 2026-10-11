def total(items)
  sum = 0
  for it in items
    next if it.nil?
    if it > 100
      sum += 100
    end
    unless it < 0
      sum += it
    end
  end
  while sum > 1000
    sum -= 1
  end
  until sum < 2000
    sum -= 1
  end
  case sum
  when 0 then return nil
  when 1 then return 1
  end
  sum > 0 && sum < 10 ? sum : 0
rescue ArgumentError
  0
end

module Shop
  def self.name
    "shop"
  end

  def helper(a)
    a and a.valid? or false
  end
end

class Cart
  def initialize
    @items = []
  end

  def size
    @items.empty? ? 0 : @items.size
  end
end
