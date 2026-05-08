// CloudFront Function — viewer-request handler.
// Adapted from tripwire/aws-setup/cloudfront-bff-function/tripwires-cookie-to-auth.js.
//
// 1. Transforms `auth_token` cookie → `Authorization: Bearer <token>` header so the
//    upstream API GW + Lambda chain can be cached and authenticated identically.
// 2. Stamps `x-original-host` from the request Host header BEFORE CloudFront overwrites
//    Host with the origin's domain. Lambda@Edge on origin-request reads this to know
//    which preview branch the user hit.

function handler(event) {
    var request = event.request;
    var headers = request.headers;

    // Stamp the original host so Lambda@Edge origin-request can route by preview env.
    if (headers.host && headers.host.value) {
        headers['x-original-host'] = { value: headers.host.value };
    }

    // Cookie → Authorization header transform.
    if (headers.cookie && headers.cookie.value) {
        var cookies = headers.cookie.value.split(';');
        for (var i = 0; i < cookies.length; i++) {
            var parts = cookies[i].trim().split('=');
            if (parts[0] === 'auth_token' && parts[1]) {
                headers.authorization = { value: 'Bearer ' + parts[1] };
                break;
            }
        }
    }

    return request;
}
