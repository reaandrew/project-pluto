'use strict';

// Lambda@Edge — origin-request handler for the preview frontend distribution.
// Adapted from smm/aws-setup/cloudfront-origin-router/index.js.
//
// Traffic comes in as preview.agency.andrewreaassociates.com/<env>/<rest>.
// We need to rewrite the origin S3 path so /<env>/<rest> is served from
// s3://ai-website-agency-frontend-preview-shared/<env>/<rest>.
// AND fall back to /<env>/index.html for client-side routing.

const PREVIEW_BUCKET_HOST = 'ai-website-agency-frontend-preview-shared-276447169330.s3.eu-west-2.amazonaws.com';
const ENV_PATTERN = /^\/([a-z0-9-]{1,31})(\/.*)?$/;

exports.handler = async (event) => {
    const request = event.Records[0].cf.request;
    const uri = request.uri || '/';

    // Reject the bare root — we don't have a directory listing.
    if (uri === '/' || uri === '') {
        return {
            status: '404',
            statusDescription: 'Not Found',
            headers: { 'content-type': [{ key: 'Content-Type', value: 'text/plain' }] },
            body: 'Preview root requires an environment prefix: /<branch>/'
        };
    }

    const match = uri.match(ENV_PATTERN);
    if (!match) {
        return {
            status: '404',
            statusDescription: 'Not Found',
            headers: { 'content-type': [{ key: 'Content-Type', value: 'text/plain' }] },
            body: 'Invalid preview path: ' + uri
        };
    }

    const env = match[1];
    let rest = match[2] || '/';

    // SPA fallback — anything that looks like a route (no extension) goes to index.html.
    // Assets under /assets/ keep their literal path.
    if (!/\.[a-zA-Z0-9]{1,8}$/.test(rest)) {
        rest = '/index.html';
    }

    request.uri = '/' + env + rest;
    return request;
};
