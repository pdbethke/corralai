using System.Collections.Generic;

interface IPriced
{
    int Price();
}

public class Cart
{
    private List<int> items = new List<int>();

    public int Size()
    {
        if (items.Count == 0)
        {
            return 0;
        }
        return items.Count;
    }

    private void Clear()
    {
        items.Clear();
    }
}
