# chatstrata documentation

Go application guides, published to GitHub Pages and served with the installer
at `https://chatstrata.sprawlcommons.com/` using Cloudflare static assets.
Zensical is optional documentation tooling; the application and tests are Go-only.

From this directory:

```sh
uv run --group dev zensical build --clean
cp ../scripts/install.sh site/install.sh
cmp ../scripts/install.sh site/install.sh
npx wrangler@4.156.0 deploy --dry-run
npx wrangler@4.156.0 deploy
```

Use a Wrangler installation logged into the domain's Cloudflare account.
The checked-in configuration publishes only `site/`, uses the ChatStrata
subdomain, and disables usage metrics and dependency instrumentation. It does
not publish archives, source transcripts, repository credentials, or an MCP
server. Never commit OAuth credentials or personal API tokens.

The documentation workflow builds and checks the same installer copy before
uploading its GitHub Pages artifact. Cloudflare deployment is separate unless
a scoped CI token/Workers build integration is configured.
