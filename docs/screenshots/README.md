# Terminal capture

`relay.txt` and `relay.svg` show the actual Relay widgets drawn to a tcell
SimulationScreen at 100 columns × 30 rows. Device names, addresses, and transfer
progress are fictional fixture data; this is not a claim of a live Tailnet run.
The SVG preserves the captured cells, colors, and selection styles.

Regenerate from the repository root:

```sh
RELAY_TUI_CAPTURE="$PWD/docs/screenshots" go test ./internal/tui -run TestCaptureScreen -v
```

Use a UTF-8 locale and leave `NO_COLOR` unset for the color version.
