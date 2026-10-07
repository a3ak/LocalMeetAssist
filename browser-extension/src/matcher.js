/* Wildcards are literal except for *. This is intentionally NOT a hostname check. */
(() => {
  const ns = globalThis.LMA ||= {};
  ns.compilePatterns = patterns => patterns.map(pattern => new RegExp(
    '^' + pattern.split('*').map(part => part.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')).join('[\\s\\S]*') + '$', 'i'
  ));
  ns.matchingUrls = (tabs, expressions) => [...new Set(tabs
    .map(tab => tab.url)
    .filter(url => typeof url === 'string' && expressions.some(re => re.test(url))))].sort();
})();
