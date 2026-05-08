'use strict';

// Lambda@Edge — origin-request handler for the preview BFF distribution.
// Inspects x-original-host (stamped by the cookie_to_auth CloudFront Function on
// viewer-request) and rewrites the origin to the matching per-branch API GW custom
// domain.
//
//   bff.agency.andrewreaassociates.com          → api.agency.andrewreaassociates.com          (production, defensive)
//   <env>.bff.agency.andrewreaassociates.com    → api-<env>.agency.andrewreaassociates.com     (preview)
//
// We MUST read x-original-host because by origin-request time CloudFront has already
// overwritten the Host header with the (placeholder) origin domain.

const HOST_PATTERN = /^([a-z0-9-]{1,31})\.bff\.website-agency\.levantar\.ai$/;

exports.handler = async (event) => {
    const request = event.Records[0].cf.request;
    const headers = request.headers;

    const originalHost =
        (headers['x-original-host'] && headers['x-original-host'][0] && headers['x-original-host'][0].value) ||
        (headers.host && headers.host[0] && headers.host[0].value) ||
        '';

    // Defensive — if we somehow get the production host on this distribution, just pass through.
    if (originalHost === 'bff.agency.andrewreaassociates.com') {
        return request;
    }

    const m = originalHost.match(HOST_PATTERN);
    if (!m) {
        return {
            status: '404',
            statusDescription: 'Not Found',
            headers: { 'content-type': [{ key: 'Content-Type', value: 'text/plain' }] },
            body: 'Unknown BFF host: ' + originalHost
        };
    }

    const env = m[1];
    const apiHost = 'api-' + env + '.agency.andrewreaassociates.com';

    request.origin = {
        custom: {
            domainName: apiHost,
            port: 443,
            protocol: 'https',
            path: '',
            sslProtocols: ['TLSv1.2'],
            readTimeout: 30,
            keepaliveTimeout: 5,
            customHeaders: {}
        }
    };
    request.headers['host'] = [{ key: 'Host', value: apiHost }];
    return request;
};
