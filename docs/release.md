# Releases

chatstrata is released as native Go binaries. Follow
[the release procedure](rewrite/release.md) for the platform matrix, embedded
search extension, installer, checksums, and release smoke checks.

The Python application and its PyPI workflow have been retired. Its last source
is preserved at the `python-final` Git tag; existing published versions remain
available. New releases use `v2.*` tags and the Go module path
`github.com/brandonbosch/chatstrata/v2`.

## PyPI status

PyPI already has the Python `0.4.0` wheel and source distribution, uploaded
on October 5, 2026. No new wheel is needed for the Go application. Keep those
historical releases available; do not use `pip install chatstrata` to install v2.
Use the [installer](https://chatstrata.sprawlcommons.com/getting-started/) or
Go source installation instead. See [ADR 0006](adr/0006-go-only-cutover.md).
