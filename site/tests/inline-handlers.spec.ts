// SPDX-License-Identifier: Elastic-2.0
import { test, expect } from '@playwright/test';

// The replay player builds inline handlers, onclick="fn(<value>)", from agent
// names and task keys in a recorded tape. They used to escape only the single
// quote, so a value ending in a backslash, or holding a double quote, ran as
// script. jsArg (replay-player.js) is now the one way a value enters a
// handler. This builds a handler exactly as the player does, in a real
// browser, and checks that the handler receives the value byte for byte and
// that nothing in the value runs.
const hostile = [
  `plain-key`,
  `it's`,
  `ends-with-backslash\\`,
  `x\\');window.__pwned=1;//`,
  `x');window.__pwned=1;//`,
  `" onmouseover="window.__pwned=1" x="`,
  `"><img src=x onerror="window.__pwned=1">`,
  `</span><script>window.__pwned=1</script>`,
  `&quot;);window.__pwned=1;//`,
  `&#39;);window.__pwned=1;//`,
  `line\nbreak sep`,
  `${'${'}window.__pwned=1}`,
];

test('jsArg carries any value into an inline handler intact and inert', async ({ page }) => {
  await page.goto('/');
  await expect.poll(() => page.evaluate(() => typeof (window as any).jsArg)).toBe('function');
  for (const v of hostile) {
    const got = await page.evaluate((value) => {
      const w = window as any;
      delete w.__pwned;
      w.__got = undefined;
      w.__probe = (x: string) => { w.__got = x; };
      const host = document.createElement('div');
      host.innerHTML = '<span id="__h" onclick="__probe(' + w.jsArg(value) + ')">x</span>';
      document.body.appendChild(host);
      const el = host.querySelector('#__h') as HTMLElement | null;
      el?.click();
      el?.dispatchEvent(new MouseEvent('mouseover'));
      host.remove();
      return { got: w.__got, pwned: w.__pwned, attrs: el ? el.getAttributeNames() : null };
    }, v);
    expect(got.pwned, `value ${JSON.stringify(v)} ran script`).toBeUndefined();
    expect(got.attrs, `value ${JSON.stringify(v)} changed the element's attributes`).toEqual(['id', 'onclick']);
    expect(got.got, `value ${JSON.stringify(v)} reached the handler altered`).toBe(v);
  }
});

// The same check against the old escaping, so this test is known to fail on
// the shape it replaced.
test('the old single-quote escaping is what jsArg replaced (negative control)', async ({ page }) => {
  await page.goto('/');
  const pwned = await page.evaluate(() => {
    const w = window as any;
    delete w.__pwned;
    w.__probe = () => {};
    const v = `x\\');window.__pwned=1;//`;
    const host = document.createElement('div');
    host.innerHTML = '<span id="__h" onclick="__probe(\'' + v.replace(/'/g, "\\'") + '\')">x</span>';
    document.body.appendChild(host);
    (host.querySelector('#__h') as HTMLElement).click();
    host.remove();
    return w.__pwned;
  });
  expect(pwned).toBe(1);
});
