# Browser asset sources

`css/` and `js/` contain the ordered authoring sources for the two shared
browser bundles. The server still embeds and serves exactly one `app.css` and
one `app.js`; no runtime bundler or additional browser request is introduced.

After changing a source part, rebuild the embedded outputs from the repository
root:

```text
go generate ./internal/web/assets
```

`go test ./...` verifies byte-for-byte that the generated outputs are current.
Keep the order in `bundle.Specs`: it is part of the CSS cascade and JavaScript
listener contract.
