// CloudFront Function — viewer-request handler (runtime: cloudfront-js-2.0).
//
// 1. Transforms the `auth_token` cookie → `Authorization: Bearer <token>`
//    header so the upstream API GW JWT authorizer + Lambda chain
//    authenticate the operator's cookie session.
// 2. Stamps `x-original-host` from the request Host header BEFORE
//    CloudFront overwrites Host with the origin's domain. The preview
//    Lambda@Edge (origin-request) reads it to route by preview env.
//
// RUNTIME NOTE (load-bearing): under cloudfront-js-2.0 cookies are NOT
// in request.headers.cookie — they are a dedicated `request.cookies`
// object, and the runtime REJECTS a returned request whose `headers`
// still contains a `cookie` entry (function error "MisplacedCookies").
// The original 1.0-style `headers.cookie` parse made this function
// error out on EVERY request, so no Authorization header was ever
// added and every authenticated BFF route 401'd (only the public
// /health route worked, which masked it). Read from request.cookies;
// never put `cookie` back onto headers.

function handler(event) {
    var request = event.request;
    var headers = request.headers;
    var cookies = request.cookies;

    // Stamp the original host so the preview Lambda@Edge can route.
    if (headers.host && headers.host.value) {
        headers['x-original-host'] = { value: headers.host.value };
    }

    // Cookie → Authorization header transform (2.0 cookies API).
    // Trim the value: a stray space/CR in the cookie would otherwise
    // produce a malformed Authorization header (silent JWT-validation
    // failure or header injection).
    if (cookies && cookies.auth_token && cookies.auth_token.value) {
        var token = cookies.auth_token.value.trim();
        if (token) {
            headers.authorization = { value: 'Bearer ' + token };
        }
    }

    return request;
}
