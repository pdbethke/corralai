// REQUIRED but never called. The module wrapper runs; no named function does.
function neverCalled(x) { return x * 2; }
module.exports = { neverCalled };
