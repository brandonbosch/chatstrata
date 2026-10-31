# chatstrata documentation

Go application guides, published to GitHub Pages and served with the installer
at `https://chatstrata.sprawlcommons.com/` using Cloudflare static assets.
Zensical is optional documentation tooling; the application and tests are Go-only.

From the repository root:

```sh
sh scripts/build-site.sh
npx wrangler@4.156.0 deploy --config docs-site/wrangler.jsonc --dry-run
npx wrangler@4.156.0 deploy --config docs-site/wrangler.jsonc
```

Use a Wrangler installation logged into the domain's Cloudflare account.
The checked-in configuration publishes only `site/`, uses the ChatStrata
subdomain, and disables usage metrics and dependency instrumentation. It does
not publish archives, source transcripts, repository credentials, or an MCP
server. Never commit OAuth credentials or personal API tokens.

The build overlays the standalone landing page in `../site/` on the generated
documentation. Guides keep their existing paths (`/getting-started/`, `/mcp/`,
and so on). The landing page uses local assets only: a WebGL2 scene, an SVG
fallback, and the bundled OFL-licensed font. It respects reduced motion and
provides a pause control. Palette preference is stored only in localStorage.

The documentation workflow builds and checks the same installer copy before
uploading its GitHub Pages artifact. Cloudflare deployment is separate unless
a scoped CI token/Workers build integration is configured.
