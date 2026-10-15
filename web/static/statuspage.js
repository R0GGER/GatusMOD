// GatusMOD status page on the main site (/status/{slug}). Loaded before the Gatus scripts: it keeps ?status={slug}
// on every page of the dashboard and sends the slug with the API requests, so GatusMOD only returns the endpoints
// of the status page.
(function () {
  var slug = window.gatusmodStatusPage;
  if (!slug) {
    return;
  }
  var HEADER = 'X-GatusMOD-Status-Page';

  function isPage(path) {
    return path === '/' || path.indexOf('/endpoints/') === 0 || path.indexOf('/suites/') === 0;
  }

  function withStatus(url) {
    var u;
    try {
      u = new URL(url, location.href);
    } catch (e) {
      return url;
    }
    if (u.origin !== location.origin || !isPage(u.pathname)) {
      return url;
    }
    u.searchParams.set('status', slug);
    return u.pathname + u.search + u.hash;
  }

  // Gatus' router only knows / and the endpoint pages.
  var start = location.pathname.indexOf('/status/') === 0 ? '/' + location.hash : location.href;
  history.replaceState(history.state, '', withStatus(start));

  ['pushState', 'replaceState'].forEach(function (name) {
    var original = history[name];
    history[name] = function (state, title, url) {
      return original.call(this, state, title, url == null ? url : withStatus(String(url)));
    };
  });

  var originalFetch = window.fetch;
  window.fetch = function (input, init) {
    var url = typeof input === 'string' ? input : input instanceof URL ? input.href : input && input.url;
    try {
      var u = new URL(url, location.href);
      if (u.origin === location.origin && (u.pathname.indexOf('/api/') === 0 || u.pathname.indexOf('/mod/api/') === 0)) {
        init = Object.assign({}, init);
        var headers = new Headers(init.headers || (input instanceof Request ? input.headers : undefined));
        headers.set(HEADER, slug);
        init.headers = headers;
      }
    } catch (e) {
      // Not a URL we scope.
    }
    return originalFetch.call(this, input, init);
  };
})();
